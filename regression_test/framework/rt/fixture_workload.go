package rt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// ExtraPorts are the workload's named ports beyond Port ("http"), set
	// for EchoMultiPort templates; nil otherwise.
	ExtraPorts []workloads.NamedPort
}

// ServiceURL is the cluster-DNS URL of the workload's service, reachable
// while connected: http://<service>.<namespace>:<port>.
func (w *Workload) ServiceURL() string {
	return fmt.Sprintf("http://%s.%s:%d", w.SvcName, w.Namespace, w.Port)
}

// ServiceURLNamed is ServiceURL for a named port other than the primary
// "http" one (EchoMultiPort). ok is false when name isn't one of the
// workload's ports.
func (w *Workload) ServiceURLNamed(name string) (url string, ok bool) {
	if name == "http" {
		return w.ServiceURL(), true
	}
	for _, p := range w.ExtraPorts {
		if p.Name == name {
			return fmt.Sprintf("http://%s.%s:%d", w.SvcName, w.Namespace, p.Port), true
		}
	}
	return "", false
}

// WorkloadFixture renders tpl in ns, applies it, and waits for the rollout.
// Keyed by (namespace, template), so two suites requesting the identical
// workload share it. Exported so a suite needing a workload outside the
// shared AppNamespace (a PrivateNamespace, or a SecondaryManager's) can call
// rt.Get(t, rt.WorkloadFixture(ns, tpl)) directly; Suite.Workload only
// covers AppNamespace, so no separate arbitrary-namespace accessor exists.
func WorkloadFixture(ns string, tpl workloads.Template) *Fixture[*Workload] {
	h := sha256.Sum256([]byte(workloadKey(ns, tpl)))
	hash := hex.EncodeToString(h[:])
	name := fmt.Sprintf("%s%s/%s", workloadFixturePrefix, ns, tpl.Name)
	return &Fixture[*Workload]{
		Name: name,
		Hash: hash,
		ProvisionFn: func(e Env) (*Workload, error) {
			return provisionWorkload(e, ns, tpl)
		},
		DestroyFn: destroyWorkload,
		// Workloads are cheap to recreate (~2-3s apply+rollout) and a full
		// run touches dozens of them: keeping them across runs marches the
		// node toward kubelet's pod limit. The warm-start win lives in the
		// manager/connection fixtures, so workloads are always torn down.
		AlwaysDestroy: true,
	}
}

// workloadKey canonicalizes tpl's identity for WorkloadFixture's hash.
func workloadKey(ns string, tpl workloads.Template) string {
	extra := make([]string, len(tpl.ExtraPorts))
	for i, p := range tpl.ExtraPorts {
		extra[i] = fmt.Sprintf("%s:%d", p.Name, p.Port)
	}
	annoKeys := make([]string, 0, len(tpl.Annotations))
	for k := range tpl.Annotations {
		annoKeys = append(annoKeys, k)
	}
	sort.Strings(annoKeys)
	annos := make([]string, len(annoKeys))
	for i, k := range annoKeys {
		annos[i] = fmt.Sprintf("%s=%s", k, tpl.Annotations[k])
	}
	envKeys := make([]string, 0, len(tpl.Env))
	for k := range tpl.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	env := make([]string, len(envKeys))
	for i, k := range envKeys {
		env[i] = fmt.Sprintf("%s=%s", k, tpl.Env[k])
	}
	return fmt.Sprintf("workload|%s|%s|%s|%d|%s|%s|headless=%t|noservice=%t|udp=%t|extra=%s|annotations=%s|"+
		"resources=%s,%s,%s,%s|appprotocol=%s|configvolume=%s,%s,%s,%s|env=%s",
		ns, tpl.Name, tpl.Kind, tpl.Replicas, tpl.Image, tpl.SvcName,
		tpl.Headless, tpl.NoService, tpl.UDP, strings.Join(extra, ","), strings.Join(annos, ","),
		tpl.Resources.Requests.CPU, tpl.Resources.Requests.Memory,
		tpl.Resources.Limits.CPU, tpl.Resources.Limits.Memory, tpl.AppProtocol,
		tpl.ConfigVolume.Name, tpl.ConfigVolume.Key, tpl.ConfigVolume.Content, tpl.ConfigVolume.MountPath,
		strings.Join(env, ","))
}

func provisionWorkload(e Env, ns string, tpl workloads.Template) (*Workload, error) {
	manifest, err := tpl.Render(ns)
	if err != nil {
		return nil, fmt.Errorf("workload %s: rendering: %w", tpl.Name, err)
	}
	// Applied directly (rather than through applyManifest) so the apply
	// output is available below to detect whether anything changed.
	tag := "workload-" + ns + "-" + tpl.Name
	path := filepath.Join(e.R.ArtifactDir("manifests"), tag+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return nil, fmt.Errorf("workload %s: writing manifest: %w", tpl.Name, err)
	}
	out, err := e.R.Kubectl(e.Ctx, ns, "apply", "-f", path)
	if err != nil {
		return nil, fmt.Errorf("workload %s: %w", tpl.Name, err)
	}
	kindPath := strings.ToLower(tpl.Kind) + "/" + tpl.Name
	if strings.Contains(out, "configured") {
		// The manager preserves a workload's existing agent config when it
		// regenerates after a template change, so annotation-driven settings
		// (inject-container-ports in particular) would not take effect on an
		// already-agented workload. Recreating the workload converges.
		if _, err := e.R.Kubectl(e.Ctx, ns, "delete", kindPath, "--ignore-not-found", "--wait"); err != nil {
			return nil, fmt.Errorf("workload %s: %w", tpl.Name, err)
		}
		if _, err := e.R.Kubectl(e.Ctx, ns, "apply", "-f", path); err != nil {
			return nil, fmt.Errorf("workload %s: %w", tpl.Name, err)
		}
	}
	if _, err := e.R.Kubectl(e.Ctx, ns, "rollout", "status", kindPath, "--timeout=120s"); err != nil {
		return nil, fmt.Errorf("workload %s: %w", tpl.Name, err)
	}
	return &Workload{
		Name:       tpl.Name,
		Namespace:  ns,
		Kind:       tpl.Kind,
		Port:       int(tpl.Port),
		SvcName:    tpl.SvcName,
		ExtraPorts: tpl.ExtraPorts,
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
	if w.SvcName == "" {
		return nil
	}
	_, err := e.R.Kubectl(e.Ctx, w.Namespace, "delete", "service", w.SvcName, "--ignore-not-found")
	return err
}
