package quicforwarder

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// DisplayName is used in the startup log line, matching the convention set by
// agent.DisplayName and manager.DisplayName.
var DisplayName = "OSS QUIC Forwarder" //nolint:gochecknoglobals // extension point

// Main is the entry point for the "quic-forwarder" subcommand (see cmd/traffic/main.go
// for how it's dispatched), following the same LoadEnv -> sigctx.DoWithSignalHandler ->
// log.NewGroup -> g.Wait() shape as manager.Main and agent.Main.
func Main(ctx context.Context, _ ...string) error {
	debug.SetTraceback("single")
	env, err := LoadEnv(nil)
	if err != nil {
		return fmt.Errorf("unable to load config: %w", err)
	}
	clog.SetTreeLevel(ctx, env.LogLevel)
	clog.Infof(ctx, "%s %s", DisplayName, version.Version)

	return sigctx.DoWithSignalHandler(ctx, func(ctx context.Context) error {
		return Run(ctx, env)
	})
}

// Run builds the Forwarder and its backend-allowlist watcher and runs both until ctx is
// done.
func Run(ctx context.Context, env *Env) error {
	allowlist := NewAllowlist(env.BackendPort)
	fwd, err := Listen(env, allowlist)
	if err != nil {
		return err
	}
	clog.Infof(ctx, "quic-forwarder listening on %s, manager backend port fallback %d, manager at %s, health on :%d",
		fwd.Addr(), env.BackendPort, env.ManagerAddress(), env.HealthPort)

	g := log.NewGroup(ctx)
	g.Go("allowlist", func(ctx context.Context) error {
		return WatchAllowlist(ctx, env.ManagerAddress(), allowlist)
	})
	g.Go("forwarder", func(ctx context.Context) error {
		return fwd.Serve(ctx)
	})
	g.Go("health", func(ctx context.Context) error {
		return ServeHealth(ctx, env.HealthPort, allowlist)
	})
	return g.Wait()
}
