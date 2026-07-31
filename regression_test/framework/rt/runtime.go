// Package rt is the regression-test framework core: runtime, fixture engine,
// suite registry, and the concrete fixtures (namespace, manager, workload,
// connection, local service) suites build on.
package rt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
)

// Runtime is the single process-global handle to the run's configuration,
// directories, and logging. Obtain it via R() once rt.Main has started.
type Runtime struct {
	ctx context.Context

	// exe is the CLI binary used for every `telepresence` invocation except
	// `telepresence helm ...`: the built binary, or (RTEST_CLIENT_VERSION
	// set to something else) a released binary downloaded by downloadBinary.
	exe string
	// helmExe drives `telepresence helm ...` specifically: always the built
	// binary, regardless of RTEST_CLIENT_VERSION, mirroring
	// integration_test/itest/helm.go:296's reasoning (an old client binary
	// may not even understand --version).
	helmExe string
	// version is the effective CLIENT version: RTEST_CLIENT_VERSION,
	// parsed, or builtVersion. Matches what Exe() reports.
	version semver.Version
	// mgrVersion is the effective MANAGER/chart version: RTEST_MANAGER_VERSION,
	// parsed, or builtVersion. Independent of version (mirrors itest's
	// separate ClientVersion/ManagerVersion defaults, both of which fall
	// back to the built version, not to each other).
	mgrVersion semver.Version
	// builtVersion is the version this run's own built binary reports,
	// detected once at startup. Used to decide whether RTEST_MANAGER_VERSION
	// names a real override (managerVersionPinned) worth pulling a released
	// chart and images for.
	builtVersion semver.Version
	registry     string
	// managerRegistry sources a pinned RTEST_MANAGER_VERSION's manager/agent
	// images (RTEST_MANAGER_REGISTRY, default ghcr.io/telepresenceio);
	// unused unless managerVersionPinned.
	managerRegistry string

	kubeconfig string
	kubeCtx    string

	// clientVersionOverride/managerVersionOverride/agentVersionOverride hold
	// the raw RTEST_*_VERSION strings, empty if unset. clientVersionOverride
	// and managerVersionOverride are resolved into version/mgrVersion at
	// startup (newRuntime); agentVersionOverride is parsed but not yet acted
	// on (no independent agent-image path exists in the catalog yet).
	clientVersionOverride  string
	managerVersionOverride string
	agentVersionOverride   string

	ci    bool
	fresh bool
	// managerRolled is set once a manager provision replaces the release's
	// pod within this run: connection adoption would re-bless a daemon whose
	// session still targets the dead pod, so it is disabled from then on.
	managerRolled bool
	teardown      bool
	tailLogs      bool
	cover         bool
	coverDirReady bool

	labels     map[Label]bool
	skipLabels map[Label]bool

	root        string // repo root
	buildOutput string
	runID       string
	artifactDir string
	configDir   string
	logDir      string

	logMu   sync.Mutex
	logFile *os.File

	engine   *engine
	manifest *manifestState

	state *stateFile

	clusterKeyOnce sync.Once
	clusterKeyVal  string
}

//nolint:gochecknoglobals // single process-wide Runtime, set by Main
var globalRuntime *Runtime

// R returns the process-global Runtime. It panics if called before Main.
func R() *Runtime {
	if globalRuntime == nil {
		panic("rt: R() called before rt.Main")
	}
	return globalRuntime
}

// newRuntime builds the Runtime for one run: resolves paths and env,
// prepares directories, writes the baseline client config, and detects the
// version under test.
func newRuntime(ctx context.Context) (*Runtime, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	env := loadEnv(root)

	runID := time.Now().UTC().Format("20060102T150405Z")
	buildOutput := filepath.Join(root, "build-output")
	artifactDir := filepath.Join(buildOutput, "rtest", "logs", runID)
	homeDir := filepath.Join(buildOutput, "rtest", "home")
	configDir := filepath.Join(homeDir, "config")
	logDir := filepath.Join(homeDir, "logs")

	for _, d := range []string{artifactDir, configDir, logDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("rtest: creating %s: %w", d, err)
		}
	}

	logFile, err := os.Create(filepath.Join(artifactDir, "run.log"))
	if err != nil {
		return nil, fmt.Errorf("rtest: creating run.log: %w", err)
	}

	r := &Runtime{
		ctx:                    ctx,
		exe:                    env.executable,
		helmExe:                env.executable,
		registry:               env.registry,
		managerRegistry:        env.managerRegistry,
		kubeconfig:             env.kubeconfig,
		kubeCtx:                env.context,
		clientVersionOverride:  env.clientVersionOverride,
		managerVersionOverride: env.managerVersionOverride,
		agentVersionOverride:   env.agentVersionOverride,
		ci:                     env.ci,
		fresh:                  env.fresh,
		teardown:               env.teardown,
		tailLogs:               env.tailLogs,
		cover:                  env.cover,
		labels:                 env.labels,
		skipLabels:             env.skipLabels,
		root:                   root,
		buildOutput:            buildOutput,
		runID:                  runID,
		artifactDir:            artifactDir,
		configDir:              configDir,
		logDir:                 logDir,
		logFile:                logFile,
	}
	r.engine = newEngine()
	r.manifest = newManifestState(runID)

	if err := r.pinContextKubeconfig(); err != nil {
		return nil, err
	}

	if err := r.writeBaselineConfig(); err != nil {
		return nil, err
	}

	// builtVersion is always detected against the built binary (helmExe),
	// never a downloaded one: it is the anchor every "is this actually an
	// override" comparison (managerVersionPinned, helmVersionArgs) is made
	// against.
	built, err := detectVersion(ctx, r.helmExe, r.childEnv())
	if err != nil {
		return nil, err
	}
	r.builtVersion = built

	r.version = built
	if r.clientVersionOverride != "" {
		v, err := semver.Parse(strings.TrimPrefix(r.clientVersionOverride, "v"))
		if err != nil {
			return nil, fmt.Errorf("rtest: parsing RTEST_CLIENT_VERSION %q: %w", r.clientVersionOverride, err)
		}
		r.version = v
	}
	if !r.version.EQ(built) {
		exe, err := downloadBinary(ctx, buildOutput, r.version)
		if err != nil {
			return nil, fmt.Errorf("rtest: downloading client %s: %w", r.version, err)
		}
		r.Infof("[rtest] RTEST_CLIENT_VERSION=%s: using downloaded binary %s", r.version, exe)
		r.exe = exe
	}

	r.mgrVersion = built
	if r.managerVersionOverride != "" {
		v, err := semver.Parse(strings.TrimPrefix(r.managerVersionOverride, "v"))
		if err != nil {
			return nil, fmt.Errorf("rtest: parsing RTEST_MANAGER_VERSION %q: %w", r.managerVersionOverride, err)
		}
		r.mgrVersion = v
	}

	st, err := loadStateFile(r.stateFilePath())
	if err != nil {
		return nil, err
	}
	r.state = st

	return r, nil
}

// repoRoot walks up from the current working directory to find the module
// root (the directory containing go.mod).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("rtest: go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// Exe returns the path to the telepresence binary under test: the built
// binary, or a downloaded release when RTEST_CLIENT_VERSION names a
// different version.
func (r *Runtime) Exe() string { return r.exe }

// Version returns the effective semver of the client binary under test
// (RTEST_CLIENT_VERSION, or the built binary's own version).
func (r *Runtime) Version() semver.Version { return r.version }

// ManagerVersion returns the effective semver of the traffic-manager
// chart/images under test (RTEST_MANAGER_VERSION, or the built binary's own
// version). Independent of Version: RTEST_CLIENT_VERSION alone does not
// change which manager gets installed.
func (r *Runtime) ManagerVersion() semver.Version { return r.mgrVersion }

// managerVersionPinned reports whether RTEST_MANAGER_VERSION names a
// version other than this run's own built version: the condition for
// pulling the released chart (helmVersionArgs) and sourcing manager/agent
// images from RTEST_MANAGER_REGISTRY instead of the run's normal
// registry/tag (mergedManagerValues).
func (r *Runtime) managerVersionPinned() bool {
	return r.managerVersionOverride != "" && !r.mgrVersion.EQ(r.builtVersion)
}

// helmVersionArgs returns the ["--version", v] pair to append to a helm
// install/upgrade invocation when managerVersionPinned, or nil otherwise:
// `telepresence helm install/upgrade --version` pulls the released chart
// from oci://ghcr.io/telepresenceio/telepresence-oss (pkg/client/cli/helm/
// chart.go's loadOrPullChart) instead of the client's built-in chart.
func (r *Runtime) helmVersionArgs() []string {
	if !r.managerVersionPinned() {
		return nil
	}
	return []string{"--version", r.mgrVersion.String()}
}

// Registry returns the image registry used for manager/agent images.
func (r *Runtime) Registry() string { return r.registry }

// IsCI reports whether this run is executing under CI (GITHUB_ACTIONS=true).
func (r *Runtime) IsCI() bool { return r.ci }

// KeepResources reports whether provisioned resources should be left in
// place at the end of the run for cross-run adoption (dev mode, unless
// RTEST_TEARDOWN=1 or CI forces teardown).
func (r *Runtime) KeepResources() bool { return !r.teardown }

// ArtifactDir returns build-output/rtest/logs/<runid>/<sub...>, creating it
// if necessary.
func (r *Runtime) ArtifactDir(sub ...string) string {
	parts := append([]string{r.artifactDir}, sub...)
	dir := filepath.Join(parts...)
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// stateFilePath returns build-output/rtest/state.yaml.
func (r *Runtime) stateFilePath() string {
	return filepath.Join(r.buildOutput, "rtest", "state.yaml")
}

// contextArgs returns the ["--context", ctx] pair when RTEST_CONTEXT is set,
// or nil otherwise.
func (r *Runtime) contextArgs() []string {
	if r.kubeCtx == "" {
		return nil
	}
	return []string{"--context", r.kubeCtx}
}

// childEnv is the explicit, documented environment passed to every kubectl
// and telepresence-under-test child process.
func (r *Runtime) childEnv() []string {
	pass := []string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR"}
	env := make([]string, 0, len(pass)+8)
	for _, k := range pass {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	kubeconfig := r.kubeconfig
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig != "" {
		env = append(env, "KUBECONFIG="+kubeconfig)
	}
	env = append(env,
		"DEV_TELEPRESENCE_CONFIG_DIR="+r.configDir,
		"DEV_TELEPRESENCE_LOG_DIR="+r.logDir,
		"TELEPRESENCE_PROGRESS=plain",
	)
	if dir, ok := r.coverClientDir(); ok {
		env = append(env, "GOCOVERDIR="+dir)
	} else if gcd := os.Getenv("GOCOVERDIR"); gcd != "" {
		env = append(env, "GOCOVERDIR="+gcd)
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{
			"APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "PATHEXT",
			"ProgramFiles", "ProgramData", "SystemDrive", "USERPROFILE",
			"USERNAME", "windir",
		} {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	return env
}

// clusterKey identifies the cluster this run targets (server URL + context),
// used to key the cross-run adoption state file. Computed once and cached.
func (r *Runtime) clusterKey(ctx context.Context) string {
	r.clusterKeyOnce.Do(func() {
		server, err := r.Kubectl(ctx, "", "config", "view", "--minify", "--raw",
			"-o", "jsonpath={.clusters[0].cluster.server}")
		if err != nil {
			server = "unknown"
		}
		kubeCtx := r.kubeCtx
		if kubeCtx == "" {
			if out, err := r.Kubectl(ctx, "", "config", "current-context"); err == nil {
				kubeCtx = strings.TrimSpace(out)
			}
		}
		r.clusterKeyVal = strings.TrimSpace(server) + "|" + kubeCtx
	})
	return r.clusterKeyVal
}

// applyManifest writes manifest to a file under ArtifactDir("manifests") and
// applies it with `kubectl apply -f <path>`. Using a file (rather than
// piping through stdin) keeps Kubectl's simple args-based signature.
func (r *Runtime) applyManifest(ctx context.Context, ns, tag, manifest string) error {
	path := filepath.Join(r.ArtifactDir("manifests"), tag+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("rtest: writing %s: %w", path, err)
	}
	_, err := r.Kubectl(ctx, ns, "apply", "-f", path)
	return err
}

// Kubectl runs kubectl with the run's context and (when ns is non-empty)
// namespace, returning combined stdout. Stderr is included in the returned
// error on failure.
func (r *Runtime) Kubectl(ctx context.Context, ns string, args ...string) (string, error) {
	full := append([]string{}, r.contextArgs()...)
	if ns != "" {
		full = append(full, "-n", ns)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "kubectl", full...)
	cmd.Env = r.childEnv()
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	r.Infof("+ kubectl %s", strings.Join(full, " "))
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(full, " "), err, errb.String())
	}
	return out.String(), nil
}

// KubectlJSON runs Kubectl with an appended `-o json` and unmarshals stdout
// into out.
func (r *Runtime) KubectlJSON(ctx context.Context, ns string, out any, args ...string) error {
	full := append(append([]string{}, args...), "-o", "json")
	stdout, err := r.Kubectl(ctx, ns, full...)
	if err != nil {
		return err
	}
	return yaml.Unmarshal([]byte(stdout), out)
}

// CLI returns a cli.TP bound to this run's binary, environment, and working
// directory.
func (r *Runtime) CLI() *cli.TP {
	return &cli.TP{
		Exe:  r.exe,
		Env:  r.childEnv(),
		Dir:  r.buildOutput,
		Logf: r.Infof,
	}
}

// helmCLI returns a cli.TP bound to the BUILT binary, never a downloaded
// RTEST_CLIENT_VERSION release, for `telepresence helm ...` invocations.
// Mirrors integration_test/itest/helm.go:296's reasoning for always driving
// helm through the built executable, simplified: unconditional rather than
// gated on version divergence, which costs nothing extra when the built and
// effective client versions already match.
func (r *Runtime) helmCLI() *cli.TP {
	return &cli.TP{
		Exe:  r.helmExe,
		Env:  r.childEnv(),
		Dir:  r.buildOutput,
		Logf: r.Infof,
	}
}

// CLIWithEnv is like CLI, but applies overrides (KEY -> value) on top of the
// run's base child environment for this one *cli.TP. Used by connection
// options (ConnWithConfig, ConnWithKubeconfig) that need a different
// KUBECONFIG or config dir for a single `connect` invocation.
func (r *Runtime) CLIWithEnv(overrides map[string]string) *cli.TP {
	return &cli.TP{
		Exe:  r.exe,
		Env:  r.envWithOverrides(overrides),
		Dir:  r.buildOutput,
		Logf: r.Infof,
	}
}

// envWithOverrides returns childEnv with overrides applied: existing
// KEY=value entries are replaced in place; new keys are appended in sorted
// order so the result is deterministic.
func (r *Runtime) envWithOverrides(overrides map[string]string) []string {
	base := r.childEnv()
	if len(overrides) == 0 {
		return base
	}
	remaining := make(map[string]string, len(overrides))
	for k, v := range overrides {
		remaining[k] = v
	}
	out := make([]string, 0, len(base)+len(remaining))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if v, ok := remaining[k]; ok {
			out = append(out, k+"="+v)
			delete(remaining, k)
		} else {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(remaining))
	for k := range remaining {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+remaining[k])
	}
	return out
}

// Infof writes a progress line to stdout and to the run's run.log.
func (r *Runtime) Infof(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	r.logMu.Lock()
	defer r.logMu.Unlock()
	fmt.Println(line)
	if r.logFile != nil {
		ts := time.Now().UTC().Format(time.RFC3339)
		fmt.Fprintf(r.logFile, "%s %s\n", ts, line)
	}
}

// baselineConfig returns the per-run client config: debug log levels, usage
// reporting disabled, the timeouts the old integration_test/itest/
// cluster.go:430 withBasicConfig pinned, and the root daemon's local
// shortcut turned off (it bypasses the traffic-agent for requests
// originating on this host, which would defeat every assertion about
// agent-side behavior). writeBaselineConfig persists this as the run's
// primary config; ConnWithConfig variants (fixture_connection.go) start
// from a fresh copy and apply a delta on top.
func (r *Runtime) baselineConfig() client.Config {
	cfg := client.GetDefaultConfig()

	ll := cfg.LogLevels()
	ll.CLI = slog.LevelDebug
	ll.UserDaemon = slog.LevelDebug
	ll.RootDaemon = slog.LevelDebug
	ll.KubeAuthDaemon = slog.LevelDebug

	to := cfg.Timeouts()
	to.PrivateClusterConnect = 60 * time.Second
	to.PrivateEndpointDial = 10 * time.Second
	to.PrivateHelm = 180 * time.Second
	to.PrivateIntercept = 30 * time.Second
	to.PrivateProxyDial = 30 * time.Second
	to.PrivateRoundtripLatency = 5 * time.Second
	to.PrivateTrafficManagerAPI = 45 * time.Second
	to.PrivateTrafficManagerConnect = 30 * time.Second
	to.PrivateConnectivityCheck = 0

	cfg.Usage().Enabled = false

	// Docker-mode connections pull the containerized daemon image from the
	// configured images registry; point it at the test registry so locally
	// built images are used.
	cfg.Images().PrivateRegistry = r.registry

	ic := cfg.Intercept()
	ic.LocalShortcut = false
	ic.LocalShortcutIsGlobal = false

	return cfg
}

// writeBaselineConfig writes the per-run client config.yml (see
// baselineConfig) and quits any daemon left running under a different one.
func (r *Runtime) writeBaselineConfig() error {
	cfg := r.baselineConfig()
	data, err := cfg.MarshalYAML()
	if err != nil {
		return fmt.Errorf("rtest: marshaling baseline config: %w", err)
	}
	ctx := filelocation.WithAppUserConfigDir(context.Background(), r.configDir)
	path := filepath.Join(filelocation.AppUserConfigDir(ctx), client.ConfigFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("rtest: writing %s: %w", path, err)
	}
	return r.quitOnConfigChange(data)
}

// variantConfigDir writes a config directory for a ConnWithConfig delta,
// applied on top of the run's baseline config, and returns its path plus a
// content fingerprint that changes whenever the resulting config would. The
// directory is content-addressed, so two connections requesting an
// equivalent delta share one.
func (r *Runtime) variantConfigDir(delta func(client.Config)) (dir, fingerprint string, err error) {
	cfg := r.baselineConfig()
	delta(cfg)
	data, err := cfg.MarshalYAML()
	if err != nil {
		return "", "", fmt.Errorf("rtest: marshaling config variant: %w", err)
	}
	fingerprint = sha256Hex(data)
	dir = filepath.Join(r.buildOutput, "rtest", "home", "config-"+fingerprint[:16])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("rtest: creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, client.ConfigFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", fmt.Errorf("rtest: writing %s: %w", path, err)
	}
	return dir, fingerprint, nil
}

// quitOnConfigChange quits any running daemons when the baseline config
// differs from the one the previous run wrote: a daemon started under the old
// config would otherwise be adopted and silently keep it.
func (r *Runtime) quitOnConfigChange(configData []byte) error {
	hashPath := filepath.Join(r.configDir, "baseline.sha256")
	sum := sha256.Sum256(configData)
	cur := hex.EncodeToString(sum[:])
	prev, err := os.ReadFile(hashPath)
	if err == nil && string(prev) == cur {
		return nil
	}
	if _, _, err := r.CLI().Run(r.ctx, "quit", "-s"); err != nil {
		r.Infof("[rtest] quit after config change: %v", err)
	}
	return os.WriteFile(hashPath, []byte(cur), 0o600)
}
