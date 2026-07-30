package rt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Workload is the value a Workload fixture provides: enough to build a
// service URL, target it in an intercept/ingest, or address it with
// kubectl.
type Workload struct {
	Name      string
	Namespace string
	Kind      string
	Port      int
	SvcName   string
}

// ServiceURL is the cluster-DNS URL of the workload's service, reachable
// while connected: http://<service>.<namespace>:<port>.
func (w *Workload) ServiceURL() string {
	return fmt.Sprintf("http://%s.%s:%d", w.SvcName, w.Namespace, w.Port)
}

// WorkloadFixture renders tpl in ns, applies it, and waits for the rollout.
// Keyed by (namespace, template), so two suites requesting the identical
// workload share it.
func WorkloadFixture(ns string, tpl workloads.Template) *Fixture[*Workload] {
	key := fmt.Sprintf("workload|%s|%s|%s|%d|%s|%s", ns, tpl.Name, tpl.Kind, tpl.Replicas, tpl.Image, tpl.SvcName)
	h := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(h[:])
	name := fmt.Sprintf("workload/%s/%s", ns, tpl.Name)
	return &Fixture[*Workload]{
		Name: name,
		Hash: hash,
		ProvisionFn: func(e Env) (*Workload, error) {
			return provisionWorkload(e, ns, tpl)
		},
		DestroyFn: destroyWorkload,
	}
}

func provisionWorkload(e Env, ns string, tpl workloads.Template) (*Workload, error) {
	manifest, err := tpl.Render(ns)
	if err != nil {
		return nil, fmt.Errorf("workload %s: rendering: %w", tpl.Name, err)
	}
	tag := "workload-" + ns + "-" + tpl.Name
	if err := e.R.applyManifest(e.Ctx, ns, tag, manifest); err != nil {
		return nil, fmt.Errorf("workload %s: %w", tpl.Name, err)
	}
	kindPath := strings.ToLower(tpl.Kind) + "/" + tpl.Name
	if _, err := e.R.Kubectl(e.Ctx, ns, "rollout", "status", kindPath, "--timeout=120s"); err != nil {
		return nil, fmt.Errorf("workload %s: %w", tpl.Name, err)
	}
	return &Workload{
		Name:      tpl.Name,
		Namespace: ns,
		Kind:      tpl.Kind,
		Port:      int(tpl.Port),
		SvcName:   tpl.SvcName,
	}, nil
}

func destroyWorkload(e Env, w *Workload) error {
	if w == nil {
		return nil
	}
	kindPath := strings.ToLower(w.Kind) + "/" + w.Name
	if _, err := e.R.Kubectl(e.Ctx, w.Namespace, "delete", kindPath, "--ignore-not-found"); err != nil {
		return err
	}
	_, err := e.R.Kubectl(e.Ctx, w.Namespace, "delete", "service", w.SvcName, "--ignore-not-found")
	return err
}
