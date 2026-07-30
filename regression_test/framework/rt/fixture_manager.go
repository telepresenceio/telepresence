package rt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// ManagerHandle is the live traffic-manager release for the spec a suite
// declared via NeedsManager.
type ManagerHandle struct {
	Namespace string
	Spec      managers.Spec
}

// helmReleaseName is both the helm release name and the Deployment name the
// chart creates.
const helmReleaseName = "traffic-manager"

const serviceAccountManifest = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    purpose: tp-rtest
`

// clientRBACManifest grants the identity connections use with --as
// (managers.TestServiceAccount) the RBAC a telepresence user needs: modeled
// on the now-deleted integration_test/testdata/k8s/client_rbac.yaml. It is
// broader than the chart's own clientRbac feature (which, as of this
// writing, omits deployments/replicasets/statefulsets/services access), so
// it is granted independently rather than relied upon.
const clientRBACManifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: rtest-test-developer
  labels:
    purpose: tp-rtest
rules:
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list", "get"]
  - apiGroups: [""]
    resources: ["pods/portforward"]
    verbs: ["create"]
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets", "statefulsets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["namespaces", "services"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: rtest-test-developer
  labels:
    purpose: tp-rtest
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[2]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: rtest-test-developer
`

// ManagerFixture is the shared rtest-manager traffic-manager release for
// spec. All manager specs share ONE release; switching specs is an in-place
// helm upgrade, so consecutive suites declaring the same spec are free.
func ManagerFixture(spec managers.Spec) *Fixture[*ManagerHandle] {
	return &Fixture[*ManagerHandle]{
		Name:        "manager/" + spec.Key,
		Hash:        spec.Hash(),
		ProvisionFn: func(e Env) (*ManagerHandle, error) { return provisionManager(e, spec) },
		AdoptFn:     func(e Env) (*ManagerHandle, bool) { return adoptManager(e, spec) },
		DestroyFn:   destroyManager,
	}
}

// mergedManagerValues layers spec's overlay on top of the runtime's
// baseline (registry/tag/pullPolicy derived from the version under test),
// then, in coverage mode (RTEST_COVER=1), adds the GOCOVERDIR env var and
// hostPath volume defined in cover.go.
func mergedManagerValues(r *Runtime, spec managers.Spec) managers.Values {
	base := managers.Baseline(r.Registry(), r.Version().String(), pullPolicyFor(r.Registry()), "true")
	values := managers.Merge(base, spec.Values)
	if r.cover {
		values = applyCoverManagerValues(values)
	}
	return values
}

func provisionManager(e Env, spec managers.Spec) (*ManagerHandle, error) {
	r := e.R
	ns := Get(e.T, managerNamespaceFixture())
	if err := ensureManagerRBAC(e, ns); err != nil {
		return nil, err
	}

	values := mergedManagerValues(r, spec)
	valuesYAML, err := yaml.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("manager/%s: marshaling values: %w", spec.Key, err)
	}
	valuesPath := filepath.Join(r.ArtifactDir("manager"), "values-"+spec.Key+".yaml")
	if err := os.WriteFile(valuesPath, valuesYAML, 0o644); err != nil {
		return nil, fmt.Errorf("manager/%s: writing values: %w", spec.Key, err)
	}

	exists, err := managerReleaseExists(e.Ctx, r, ns)
	if err != nil {
		return nil, err
	}
	// Spec switching relies on the values file fully defining the release:
	// --reset-values keeps a previous spec's values from leaking into this one.
	args := []string{"helm", "install", "-n", ns, "-f", valuesPath}
	if exists {
		args = []string{"helm", "upgrade", "--reset-values", "-n", ns, "-f", valuesPath}
	}
	if _, stderr, err := r.CLI().Run(e.Ctx, args...); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args[:2], " "), err, stderr)
	}
	if _, err := r.Kubectl(e.Ctx, ns, "rollout", "status", "deploy/"+helmReleaseName, "--timeout=180s"); err != nil {
		return nil, err
	}

	valuesHash := sha256Hex(valuesYAML)
	if err := r.state.record(r.clusterKey(e.Ctx), spec.Hash(), &fixtureState{
		Kind:       "manager",
		Names:      []string{helmReleaseName},
		ValuesHash: valuesHash,
	}); err != nil {
		r.Infof("[rtest] manager/%s: writing state file: %v", spec.Key, err)
	}
	return &ManagerHandle{Namespace: ns, Spec: spec}, nil
}

func adoptManager(e Env, spec managers.Spec) (*ManagerHandle, bool) {
	r := e.R
	ns := managers.ManagerNamespace
	exists, err := managerReleaseExists(e.Ctx, r, ns)
	if err != nil || !exists {
		return nil, false
	}
	st := r.state.lookup(r.clusterKey(e.Ctx), spec.Hash())
	if st == nil {
		return nil, false
	}
	values := mergedManagerValues(r, spec)
	valuesYAML, err := yaml.Marshal(values)
	if err != nil || sha256Hex(valuesYAML) != st.ValuesHash {
		return nil, false
	}
	if _, err := r.Kubectl(e.Ctx, ns, "rollout", "status", "deploy/"+helmReleaseName, "--timeout=180s"); err != nil {
		return nil, false
	}
	return &ManagerHandle{Namespace: ns, Spec: spec}, true
}

func destroyManager(e Env, h *ManagerHandle) error {
	if h == nil {
		return nil
	}
	_, stderr, err := e.R.CLI().Run(e.Ctx, "helm", "uninstall", "--manager-namespace", h.Namespace)
	if err != nil && !strings.Contains(strings.ToLower(stderr), "not found") &&
		!strings.Contains(strings.ToLower(err.Error()), "not found") {
		return fmt.Errorf("helm uninstall: %w: %s", err, stderr)
	}
	return nil
}

// managerReleaseExists checks for the helm release secret directly, per the
// M1 contract, rather than shelling out to `helm list`.
func managerReleaseExists(ctx context.Context, r *Runtime, ns string) (bool, error) {
	out, err := r.Kubectl(ctx, ns, "get", "secret", "-l", "owner=helm", "-o", "name")
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "sh.helm.release.v1."+helmReleaseName+"."), nil
}

func ensureManagerRBAC(e Env, ns string) error {
	sa := fmt.Sprintf(serviceAccountManifest, managers.TestServiceAccount, ns)
	if err := e.R.applyManifest(e.Ctx, ns, "service-account", sa); err != nil {
		return err
	}
	rbac := fmt.Sprintf(clientRBACManifest, managers.TestServiceAccount, ns)
	return e.R.applyManifest(e.Ctx, "", "client-rbac", rbac)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
