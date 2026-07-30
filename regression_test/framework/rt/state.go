package rt

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// stateFile is the cross-run adoption record persisted to
// build-output/rtest/state.yaml, keyed by cluster (server URL + context)
// then by fixture hash. AdoptFn implementations consult it alongside a
// direct cluster check before reusing a resource left by a previous run.
type stateFile struct {
	mu   sync.Mutex
	path string
	data stateData
}

type stateData struct {
	Clusters map[string]*clusterState `json:"clusters,omitempty"`
}

type clusterState struct {
	Fixtures map[string]*fixtureState `json:"fixtures,omitempty"`
}

// fixtureState is one fixture's adoption record: what it created, and (for
// the manager fixture) the values hash that must match for a helm release to
// be considered equivalent.
type fixtureState struct {
	Kind       string   `json:"kind"`
	Names      []string `json:"names,omitempty"`
	ValuesHash string   `json:"valuesHash,omitempty"`
}

func loadStateFile(path string) (*stateFile, error) {
	sf := &stateFile{path: path, data: stateData{Clusters: map[string]*clusterState{}}}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return sf, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, &sf.data); err != nil {
		return nil, err
	}
	if sf.data.Clusters == nil {
		sf.data.Clusters = map[string]*clusterState{}
	}
	return sf, nil
}

func (sf *stateFile) save() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	data, err := yaml.Marshal(sf.data)
	if err != nil {
		return err
	}
	return os.WriteFile(sf.path, data, 0o644)
}

func (sf *stateFile) cluster(key string) *clusterState {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	c, ok := sf.data.Clusters[key]
	if !ok {
		c = &clusterState{Fixtures: map[string]*fixtureState{}}
		sf.data.Clusters[key] = c
	}
	return c
}

// record stores (or overwrites) the fixture entry for hash under the given
// cluster key and persists the file.
func (sf *stateFile) record(key, hash string, fs *fixtureState) error {
	c := sf.cluster(key)
	sf.mu.Lock()
	c.Fixtures[hash] = fs
	sf.mu.Unlock()
	return sf.save()
}

// lookup returns the fixture entry for hash under the given cluster key, or
// nil if there is none.
func (sf *stateFile) lookup(key, hash string) *fixtureState {
	c := sf.cluster(key)
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return c.Fixtures[hash]
}

// CleanAll deletes every resource labeled purpose=tp-rtest, uninstalls the
// rtest-manager helm release, and quits any running rtest daemons. It is
// invoked via `go run ./regression_test/framework/rtclean` (make
// rtest-clean). It builds a minimal Runtime directly rather than going
// through newRuntime/Main, since cleanup needs neither version detection
// nor a fresh per-run artifact directory.
func CleanAll(ctx context.Context) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	env := loadEnv(root)
	homeDir := filepath.Join(root, "build-output", "rtest", "home")
	r := &Runtime{
		ctx:        ctx,
		exe:        env.executable,
		kubeconfig: env.kubeconfig,
		kubeCtx:    env.context,
		configDir:  filepath.Join(homeDir, "config"),
		logDir:     filepath.Join(homeDir, "logs"),
	}

	if _, stderr, err := r.CLI().Run(ctx, "quit", "-s"); err != nil {
		r.Infof("[rtest-clean] quit -s: %v: %s", err, stderr)
	}
	_, stderr, err := r.CLI().Run(ctx, "helm", "uninstall", "--manager-namespace", managers.ManagerNamespace)
	if err != nil {
		r.Infof("[rtest-clean] helm uninstall: %v: %s", err, stderr)
	}
	kinds := "ns,svc,deploy,statefulset,clusterrole,clusterrolebinding,mutatingwebhookconfiguration,pvc,pv"
	if _, err := r.Kubectl(ctx, "", "delete", kinds,
		"-l", purposeLabelKey+"="+purposeLabelValue, "--ignore-not-found", "--wait=false"); err != nil {
		r.Infof("[rtest-clean] kubectl delete: %v", err)
	}
	// Chart-created webhook configurations carry no rtest label but are
	// cluster-scoped, so a died run's are not removed with their namespace.
	if out, err := r.Kubectl(ctx, "", "get", "mutatingwebhookconfigurations", "-o", "name"); err == nil {
		for _, name := range strings.Fields(out) {
			if strings.Contains(name, "agent-injector-webhook-rtest-") {
				_, _ = r.Kubectl(ctx, "", "delete", name, "--ignore-not-found")
			}
		}
	}
	return nil
}
