package rt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/blang/semver/v4"
)

// envConfig is the parsed, resolved set of RTEST_* environment inputs for
// one run. See the M1 contract's Environment table for the full var list.
type envConfig struct {
	kubeconfig      string
	context         string
	registry        string
	managerRegistry string
	executable      string

	clientVersionOverride  string // RTEST_CLIENT_VERSION, empty if unset
	managerVersionOverride string // RTEST_MANAGER_VERSION, empty if unset
	agentVersionOverride   string // RTEST_AGENT_VERSION, empty if unset

	labels     map[Label]bool
	skipLabels map[Label]bool

	ci       bool
	fresh    bool
	teardown bool
	tailLogs bool
	cover    bool
}

// loadEnv resolves envConfig from the process environment. The shell
// environment always wins; there is no rtest.yml in M1.
func loadEnv(root string) envConfig {
	ci := os.Getenv("GITHUB_ACTIONS") == "true"

	registry := os.Getenv("RTEST_REGISTRY")
	if registry == "" {
		registry = os.Getenv("TELEPRESENCE_REGISTRY")
	}
	if registry == "" {
		registry = "ghcr.io/telepresenceio"
	}

	// managerRegistry sources a pinned RTEST_MANAGER_VERSION's manager/agent
	// images: deliberately independent of RTEST_REGISTRY/TELEPRESENCE_REGISTRY,
	// which usually names a local or cluster-loaded registry that never holds
	// a released version's images.
	managerRegistry := os.Getenv("RTEST_MANAGER_REGISTRY")
	if managerRegistry == "" {
		managerRegistry = "ghcr.io/telepresenceio"
	}

	exe := os.Getenv("RTEST_EXECUTABLE")
	if exe == "" {
		name := "telepresence"
		if runtime.GOOS == "windows" {
			name = "telepresence.exe"
		}
		exe = filepath.Join(root, "build-output", "bin", name)
	}

	return envConfig{
		kubeconfig:             os.Getenv("RTEST_KUBECONFIG"),
		context:                os.Getenv("RTEST_CONTEXT"),
		registry:               registry,
		managerRegistry:        managerRegistry,
		executable:             exe,
		clientVersionOverride:  os.Getenv("RTEST_CLIENT_VERSION"),
		managerVersionOverride: os.Getenv("RTEST_MANAGER_VERSION"),
		agentVersionOverride:   os.Getenv("RTEST_AGENT_VERSION"),
		labels:                 parseLabelSet(os.Getenv("RTEST_LABELS")),
		skipLabels:             parseLabelSet(os.Getenv("RTEST_SKIP_LABELS")),
		ci:                     ci,
		fresh:                  ci || os.Getenv("RTEST_FRESH") == "1",
		teardown:               ci || os.Getenv("RTEST_TEARDOWN") == "1",
		tailLogs:               os.Getenv("RTEST_TAIL_LOGS") == "1",
		cover:                  os.Getenv("RTEST_COVER") == "1",
	}
}

func parseLabelSet(v string) map[Label]bool {
	set := map[Label]bool{}
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			set[Label(s)] = true
		}
	}
	return set
}

// pullPolicyFor returns the image pullPolicy implied by a registry value,
// matching the old harness's rule: "local" => Never, "localhost:*" => Always,
// any other registry => "" (no override, use the chart default).
func pullPolicyFor(registry string) string {
	switch {
	case registry == "local":
		return "Never"
	case strings.HasPrefix(registry, "localhost:"):
		return "Always"
	default:
		return ""
	}
}

// clientVersionLine matches the "Client" row emitted by `telepresence
// version` text output, e.g. "OSS Client : v2.32.0-rtest.0" or the
// enterprise "Client      : v2.32.0". The optional leading token covers the
// "OSS " / "Enterprise " display-name prefixes; a bare "Client" also
// matches.
var clientVersionLine = regexp.MustCompile(`(?m)^(?:\S+\s+)?Client\s*:\s*v?(\S+)\s*$`)

// detectVersion determines the version under test: $TELEPRESENCE_VERSION if
// set, else parsed from the client-line of `<exe> version` plain text
// output. `--output json` wraps that same text in a {cmd,stdout} envelope
// rather than a structured client field, so plain text is parsed directly;
// see docs/plans/regression-test-framework/m1-contract.md.
func detectVersion(ctx context.Context, exe string, env []string) (semver.Version, error) {
	if v := os.Getenv("TELEPRESENCE_VERSION"); v != "" {
		return semver.Parse(strings.TrimPrefix(v, "v"))
	}
	cmd := exec.CommandContext(ctx, exe, "version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return semver.Version{}, fmt.Errorf("%s version: %w", exe, err)
	}
	m := clientVersionLine.FindSubmatch(out)
	if m == nil {
		return semver.Version{}, fmt.Errorf("%s version: no Client line found in:\n%s", exe, out)
	}
	return semver.Parse(strings.TrimPrefix(string(m[1]), "v"))
}
