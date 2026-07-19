package manifest

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec" //nolint:depguard // detached handler process, not subject to soft-context handling
	"path/filepath"
	"slices"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// handlerEnv returns env augmented with the variables that the imperative commands synthesize
// for a handler process on top of the attachment's remote environment: the attachment id, the
// Telepresence API host, and the mount root. envID is the value for TELEPRESENCE_INTERCEPT_ID,
// which for an ingest differs from the id the handler is registered under.
func handlerEnv(ctx context.Context, env map[string]string, envID, mountPoint string) map[string]string {
	e := make(map[string]string, len(env)+3)
	maps.Copy(e, env)
	e["TELEPRESENCE_INTERCEPT_ID"] = envID
	e[agentconfig.EnvAPIHost] = daemon.MustGetUserClient(ctx).DaemonID().ContainerName()
	e["TELEPRESENCE_ROOT"] = mountPoint
	return e
}

// handlerRec is the client-side record of a running handler process, keyed by daemon and
// attachment name so a later apply can tell whether the declared handler is still running.
type handlerRec struct {
	Pid  int      `json:"pid"`
	Args []string `json:"args"`
}

// handlerStateFile returns the cache-relative path of the handler state file for the named
// attachment, keyed by the daemon that owns it. The attachment name is sanitized because it can
// contain path separators (a replace attachment may be named "workload/container").
func handlerStateFile(ctx context.Context, name string) string {
	id := daemon.MustGetUserClient(ctx).DaemonID()
	return filepath.Join("handlers", id.InfoFileName(), ioutil.SafeName(name)+".json")
}

// startHandler starts a.Command detached from the CLI, with env merged over the local
// environment, registers it with the daemon under handlerID so the daemon terminates it when the
// attachment is removed, and records its pid and argv for later reconciliation.
func startHandler(ctx context.Context, a *Attachment, env map[string]string, handlerID string) error {
	logDir := filelocation.AppUserLogDir(ctx)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("creating handler log dir %q: %w", logDir, err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("handler-%s.log", ioutil.SafeName(a.Name)))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening handler log %q: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(a.Command[0], a.Command[1:]...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	proc.CreateDetached(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting handler for attachment %q: %w", a.Name, err)
	}
	if err := daemon.MustGetUserClient(ctx).AddHandler(ctx, handlerID, cmd, ""); err != nil {
		return err
	}
	rec := handlerRec{Pid: cmd.Process.Pid, Args: a.Command}
	if err := cache.SaveToUserCache(ctx, rec, handlerStateFile(ctx, a.Name), cache.Private); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// handlerStatus reads the recorded handler state for a, if any, and reports whether the recorded
// process is still alive and whether its argv still matches a.Command.
func handlerStatus(ctx context.Context, a *Attachment) (running bool, argsEqual bool, rec *handlerRec) {
	var r handlerRec
	if err := cache.LoadFromUserCache(ctx, &r, handlerStateFile(ctx, a.Name)); err != nil {
		return false, false, nil
	}
	return proc.IsAlive(r.Pid), slices.Equal(r.Args, a.Command), &r
}

// stopHandler terminates a's recorded handler process, if it's still alive, and removes the
// state file. An already-dead recorded pid is not an error. It reports whether a live process was
// terminated.
func stopHandler(ctx context.Context, a *Attachment) (stopped bool) {
	file := handlerStateFile(ctx, a.Name)
	var rec handlerRec
	if err := cache.LoadFromUserCache(ctx, &rec, file); err != nil {
		return false
	}
	if proc.IsAlive(rec.Pid) {
		if p, err := os.FindProcess(rec.Pid); err == nil && proc.Terminate(p) == nil {
			stopped = true
		}
	}
	_ = cache.DeleteFromUserCache(ctx, file)
	return stopped
}
