package rt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// (managers.TestServiceAccount) the RBAC a telepresence user needs. It is
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
// baseline (registry/tag/pullPolicy derived from the manager version under
// test), then, in coverage mode (RTEST_COVER=1), adds the GOCOVERDIR env
// var and hostPath volume defined in cover.go.
func mergedManagerValues(r *Runtime, spec managers.Spec) managers.Values {
	registry := r.Registry()
	pullPolicy := pullPolicyFor(registry)
	if r.managerVersionPinned() {
		// A pinned compat manager pulls a released chart from
		// oci://ghcr.io/telepresenceio/telepresence-oss; its images come from
		// RTEST_MANAGER_REGISTRY (default the public ghcr.io/telepresenceio),
		// not this run's own registry, and Never is never forced: a released
		// image was never loaded into a local/kind registry, so forcing Never
		// would leave the release stuck ImagePullBackOff.
		registry = r.managerRegistry
		pullPolicy = pullPolicyFor(registry)
		if pullPolicy == "Never" {
			pullPolicy = ""
		}
	}
	base := managers.Baseline(registry, r.ManagerVersion().String(), pullPolicy, "true")
	values := managers.Merge(base, spec.Values)
	if r.cover {
		values = applyCoverManagerValues(values)
	}
	return values
}

// marshalManagerValues marshals values to YAML for the helm values file and
// the state-file hash. When RTEST_MANAGER_VERSION pins a released version
// (managerVersionPinned), keys the old chart's values.schema.yaml doesn't
// know about yet are pruned first (managers.PruneForVersion): its
// additionalProperties:false schema rejects an unrecognized key outright
// rather than ignoring it.
func marshalManagerValues(r *Runtime, values managers.Values) ([]byte, error) {
	data, err := yaml.Marshal(values)
	if err != nil {
		return nil, err
	}
	if !r.managerVersionPinned() {
		return data, nil
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("rtest: re-decoding manager values for pruning: %w", err)
	}
	managers.PruneForVersion(m, r.ManagerVersion())
	return yaml.Marshal(m)
}

func provisionManager(e Env, spec managers.Spec) (*ManagerHandle, error) {
	r := e.R
	ns := Get(e.T, managerNamespaceFixture())
	if err := ensureManagerRBAC(e, ns); err != nil {
		return nil, err
	}

	values := mergedManagerValues(r, spec)
	valuesYAML, err := marshalManagerValues(r, values)
	if err != nil {
		return nil, fmt.Errorf("manager/%s: marshaling values: %w", spec.Key, err)
	}
	fileKey := strings.ReplaceAll(spec.Key, "/", "-")
	valuesPath := filepath.Join(r.ArtifactDir("manager"), "values-"+fileKey+".yaml")
	if err := os.WriteFile(valuesPath, valuesYAML, 0o644); err != nil {
		return nil, fmt.Errorf("manager/%s: writing values: %w", spec.Key, err)
	}

	if r.cover {
		r.ensureCoverDirWritable(e, ns)
	}

	exists, err := managerReleaseExists(e.Ctx, r, ns)
	if err != nil {
		return nil, err
	}
	// Spec switching relies on the values file fully defining the release:
	// --reset-values keeps a previous spec's values from leaking into this one.
	verArgs := r.helmVersionArgs()
	args := make([]string, 0, 7+len(verArgs))
	if exists {
		args = append(args, "helm", "upgrade", "--reset-values", "-n", ns, "-f", valuesPath)
	} else {
		args = append(args, "helm", "install", "-n", ns, "-f", valuesPath)
	}
	args = append(args, verArgs...)
	if _, stderr, err := r.helmCLI().Run(e.Ctx, args...); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args[:2], " "), err, stderr)
	}
	if _, err := r.Kubectl(e.Ctx, ns, "rollout", "status", "deploy/"+helmReleaseName, "--timeout=180s"); err != nil {
		return nil, err
	}
	if err := waitOldManagerGone(e, ns); err != nil {
		return nil, err
	}

	// All manager specs share one release: installing this spec makes every
	// other spec's memoized handle stale. The rollout also replaced the
	// manager pod, so every live connection's session now port-forwards to a
	// dead pod — invalidate them all; the next Connect re-provisions fresh.
	r.engine.invalidateSiblings("manager/", spec.Hash())
	r.engine.invalidateSiblings("connection/", "")
	r.managerRolled = true

	valuesHash := sha256Hex(valuesYAML)
	// One record for the release, not one per spec: the release can only be
	// in one configuration, and adoption must compare against whatever the
	// LAST provision left installed, no matter which spec that was.
	if err := r.state.record(r.clusterKey(e.Ctx), managerStateKey, &fixtureState{
		Kind:       "manager",
		Names:      []string{helmReleaseName},
		ValuesHash: valuesHash,
	}); err != nil {
		r.Infof("[rtest] manager/%s: writing state file: %v", spec.Key, err)
	}
	return &ManagerHandle{Namespace: ns, Spec: spec}, nil
}

// managerStateKey is the state-file key for the single shared release.
const managerStateKey = "manager-current"

func adoptManager(e Env, spec managers.Spec) (*ManagerHandle, bool) {
	r := e.R
	ns := managers.ManagerNamespace
	exists, err := managerReleaseExists(e.Ctx, r, ns)
	if err != nil || !exists {
		return nil, false
	}
	st := r.state.lookup(r.clusterKey(e.Ctx), managerStateKey)
	if st == nil {
		return nil, false
	}
	values := mergedManagerValues(r, spec)
	valuesYAML, err := marshalManagerValues(r, values)
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
	_, stderr, err := e.R.helmCLI().Run(e.Ctx, "helm", "uninstall", "--manager-namespace", h.Namespace)
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

// RestartManager restarts the shared manager's Deployment and waits for the
// rollout to finish. A newly created or newly labeled namespace only enters
// the manager's namespaceSelector-managed set once its pod restarts and
// re-lists namespaces; there is no live pickup. Callers that create or
// label a namespace after the manager is already running must call this
// before anything that depends on the manager seeing it.
func RestartManager(e Env) error {
	ns := managers.ManagerNamespace
	if _, err := e.R.Kubectl(e.Ctx, ns, "rollout", "restart", "deploy/"+helmReleaseName); err != nil {
		return err
	}
	if _, err := e.R.Kubectl(e.Ctx, ns, "rollout", "status", "deploy/"+helmReleaseName, "--timeout=120s"); err != nil {
		return err
	}
	return waitOldManagerGone(e, ns)
}

// waitOldManagerGone waits until only one traffic-manager pod remains.
// rollout status returns while the old pod may still be terminating; both
// the webhook service and a service-to-pod resolution done for a fresh
// session can still land on it, with its pre-rollout configuration.
func waitOldManagerGone(e Env, ns string) error {
	for range 60 {
		out, err := e.R.Kubectl(e.Ctx, ns, "get", "pods", "-l", "app=traffic-manager", "-o", "name")
		if err != nil {
			return err
		}
		if len(strings.Fields(out)) <= 1 {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("old traffic-manager pod still present after rollout")
}

// parkManagerOnDefault re-provisions the shared release with the Default
// spec when the state file says something else is installed. Called at the
// end of a keep-resources run.
func (r *Runtime) parkManagerOnDefault() {
	values := mergedManagerValues(r, managers.Default)
	data, err := marshalManagerValues(r, values)
	if err != nil {
		return
	}
	st := r.state.lookup(r.clusterKey(r.ctx), managerStateKey)
	if st != nil && st.ValuesHash == sha256Hex(data) {
		return
	}
	if ok, _ := managerReleaseExists(r.ctx, r, managers.ManagerNamespace); !ok {
		return
	}
	tb := &runTB{r: r}
	if _, err := provisionManager(Env{Ctx: r.ctx, T: tb, R: r}, managers.Default); err != nil {
		r.Infof("[rtest] parking manager on the default spec: %v", err)
		return
	}
	r.Infof("[rtest] parked manager on the default spec")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
