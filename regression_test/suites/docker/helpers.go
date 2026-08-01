package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
)

// attachTimeout/attachPollInterval bound how long an intercept takes to show
// up in `list`, shared by every suite in this package that polls for one.
const (
	attachTimeout      = 30 * time.Second
	attachPollInterval = time.Second
)

// dockerOutput runs `docker <args...>` and returns its trimmed stdout: the
// one place in this package that shells out to the docker CLI, so every
// `docker inspect`/`docker volume inspect`/`docker network inspect`
// assertion goes through it instead of the moby SDK.
func dockerOutput(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// waitProcExit waits for p to exit within timeout, logging (not failing on)
// a non-nil process error: every caller in this package ends the process
// involuntarily (signal, detach, disconnect, quit), so a non-zero exit is
// expected -- only a timeout (cli.Proc.Wait killing it) is a failure.
func waitProcExit(t testing.TB, p *cli.Proc, timeout time.Duration) {
	t.Helper()
	_, stderr, err := p.Wait(timeout)
	if err != nil {
		t.Logf("docker-run process: %v\nstderr:\n%s", err, stderr)
	}
}

// present reports whether entries contains a workload named name in ns.
func present(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// attached reports whether the workload named name in ns currently carries
// an intercept or ingest.
func attached(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return len(e.InterceptInfo)+len(e.IngestInfo) > 0
		}
	}
	return false
}
