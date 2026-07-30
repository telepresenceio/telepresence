package rt

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// AppNamespace is the shared namespace suites use for application
// workloads.
const AppNamespace = "rtest-app"

// purposeLabelKey/purposeLabelValue mark every object the framework creates
// (namespaces, and the ClusterRole/ClusterRoleBinding in
// fixture_manager.go), so `make rtest-clean` can find and remove them.
const (
	purposeLabelKey   = "purpose"
	purposeLabelValue = "tp-rtest"
)

// objectMeta is the minimal shape used to inspect labels on an existing
// Kubernetes object during adoption.
type objectMeta struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

const namespaceManifest = `apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
  labels:
    purpose: tp-rtest
    rtest.telepresence.io/managed: "true"
    app.kubernetes.io/name: %[1]s
`

// namespaceFixture is a namespace with the labels the manager's
// namespaceSelector and rtest-clean rely on. It is shared by AppNamespace
// and the manager namespace (fixture_manager.go).
func namespaceFixture(name string) *Fixture[string] {
	h := sha256.Sum256([]byte("namespace/" + name))
	hash := hex.EncodeToString(h[:])
	return &Fixture[string]{
		Name: "namespace/" + name,
		Hash: hash,
		ProvisionFn: func(e Env) (string, error) {
			manifest := fmt.Sprintf(namespaceManifest, name)
			if err := e.R.applyManifest(e.Ctx, "", "namespace-"+name, manifest); err != nil {
				return "", err
			}
			return name, nil
		},
		AdoptFn: func(e Env) (string, bool) {
			var obj objectMeta
			if err := e.R.KubectlJSON(e.Ctx, "", &obj, "get", "namespace", name); err != nil {
				return "", false
			}
			ok := obj.Metadata.Labels[purposeLabelKey] == purposeLabelValue &&
				obj.Metadata.Labels[managers.ManagedNamespaceLabel] == "true"
			return name, ok
		},
		DestroyFn: func(e Env, ns string) error {
			_, err := e.R.Kubectl(e.Ctx, "", "delete", "namespace", ns, "--ignore-not-found", "--wait=false")
			return err
		},
	}
}

func appNamespaceFixture() *Fixture[string] {
	return namespaceFixture(AppNamespace)
}

func managerNamespaceFixture() *Fixture[string] {
	return namespaceFixture(managers.ManagerNamespace)
}

// PrivateNamespace provisions a namespace named rtest-<prefix>-<4hex>,
// carrying the same managed label as AppNamespace/the manager namespace, so
// a namespaceSelector matching that label picks it up. Unlike those shared
// namespaces, it is never shared across suites or adopted across runs: each
// call creates a fresh one, and it is always destroyed at run end, even in
// dev keep mode (AlwaysDestroy).
func PrivateNamespace(e Env, prefix string) string {
	e.T.Helper()
	name := fmt.Sprintf("rtest-%s-%s", prefix, randomHex(4))
	fx := &Fixture[string]{
		Name:          "namespace/" + name,
		Hash:          sha256Hex([]byte("private-namespace/" + name)),
		AlwaysDestroy: true,
		ProvisionFn: func(e Env) (string, error) {
			manifest := fmt.Sprintf(namespaceManifest, name)
			if err := e.R.applyManifest(e.Ctx, "", "namespace-"+name, manifest); err != nil {
				return "", err
			}
			return name, nil
		},
		DestroyFn: func(e Env, ns string) error {
			_, err := e.R.Kubectl(e.Ctx, "", "delete", "namespace", ns, "--ignore-not-found", "--wait=false")
			return err
		},
	}
	return Get(e.T, fx)
}

// unmanagedNamespaceManifest is namespaceManifest without the
// rtest.telepresence.io/managed label, so a namespaceSelector matching that
// label (the shared manager's) doesn't pick the namespace up.
const unmanagedNamespaceManifest = `apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
  labels:
    purpose: tp-rtest
    app.kubernetes.io/name: %[1]s
`

// PrivateUnmanagedNamespace is PrivateNamespace without the
// rtest.telepresence.io/managed label, for a namespace that must stay
// unmanaged by the shared manager (e.g. one about to get its own
// SecondaryManager, whose namespaceSelector would otherwise conflict with
// the shared manager's over that label). Same lifecycle: fresh every call,
// always destroyed at run end (AlwaysDestroy).
func PrivateUnmanagedNamespace(e Env, prefix string) string {
	e.T.Helper()
	name := fmt.Sprintf("rtest-%s-%s", prefix, randomHex(4))
	fx := &Fixture[string]{
		Name:          "namespace/" + name,
		Hash:          sha256Hex([]byte("private-unmanaged-namespace/" + name)),
		AlwaysDestroy: true,
		ProvisionFn: func(e Env) (string, error) {
			manifest := fmt.Sprintf(unmanagedNamespaceManifest, name)
			if err := e.R.applyManifest(e.Ctx, "", "namespace-"+name, manifest); err != nil {
				return "", err
			}
			return name, nil
		},
		DestroyFn: func(e Env, ns string) error {
			_, err := e.R.Kubectl(e.Ctx, "", "delete", "namespace", ns, "--ignore-not-found", "--wait=false")
			return err
		},
	}
	return Get(e.T, fx)
}

// randomHex returns n lowercase hex characters from crypto/rand.
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
