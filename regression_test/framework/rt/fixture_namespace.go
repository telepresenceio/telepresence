package rt

import (
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
