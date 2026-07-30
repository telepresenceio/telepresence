package rt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// SecondaryManager is a full traffic-manager release in ns (a namespace of
// its own, typically from PrivateNamespace), independent of the single
// shared release ManagerFixture owns. Used where a suite needs two managers
// at once (connect/Multi) or, in wave 2, a Helm-lifecycle target.
//
// It is scoped to manage only ns (namespaceSelector matches ns by name, via
// the built-in kubernetes.io/metadata.name label), so it never contends with
// the shared manager over AppNamespace or any other labeled namespace. The
// client identity every connection authenticates as (connectAs) is granted
// cluster-wide by ManagerFixture's RBAC; this fixture ensures that RBAC
// exists too, in case a suite reaches for a SecondaryManager before ever
// provisioning the shared one.
//
// Kept simple: always provisioned fresh, never adopted across runs, and
// destroyed at run end unconditionally (AlwaysDestroy), like PrivateNamespace.
func SecondaryManager(spec managers.Spec, ns string) *Fixture[*ManagerHandle] {
	h := sha256.Sum256([]byte("secondary-manager|" + ns + "|" + spec.Hash()))
	hash := hex.EncodeToString(h[:])
	return &Fixture[*ManagerHandle]{
		Name:          "secondary-manager/" + ns,
		Hash:          hash,
		AlwaysDestroy: true,
		ProvisionFn: func(e Env) (*ManagerHandle, error) {
			return provisionSecondaryManager(e, spec, ns)
		},
		DestroyFn: destroySecondaryManager,
	}
}

func provisionSecondaryManager(e Env, spec managers.Spec, ns string) (*ManagerHandle, error) {
	r := e.R
	mgrNS := Get(e.T, managerNamespaceFixture())
	if err := ensureManagerRBAC(e, mgrNS); err != nil {
		return nil, err
	}

	values := mergedManagerValues(r, spec)
	values.NamespaceSelector = &labels.Selector{
		MatchLabels: map[string]string{labels.NameLabelKey: ns},
	}
	valuesYAML, err := yaml.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("secondary-manager/%s: marshaling values: %w", ns, err)
	}
	valuesPath := filepath.Join(r.ArtifactDir("manager"), "values-secondary-"+ns+".yaml")
	if err := os.WriteFile(valuesPath, valuesYAML, 0o644); err != nil {
		return nil, fmt.Errorf("secondary-manager/%s: writing values: %w", ns, err)
	}

	args := []string{"helm", "install", "-n", ns, "-f", valuesPath}
	if _, stderr, err := r.CLI().Run(e.Ctx, args...); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args[:2], " "), err, stderr)
	}
	if _, err := r.Kubectl(e.Ctx, ns, "rollout", "status", "deploy/"+helmReleaseName, "--timeout=180s"); err != nil {
		return nil, err
	}
	return &ManagerHandle{Namespace: ns, Spec: spec}, nil
}

func destroySecondaryManager(e Env, h *ManagerHandle) error {
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
