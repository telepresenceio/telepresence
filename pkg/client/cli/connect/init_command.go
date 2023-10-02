package connect

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type cmdInitKey struct{}

func WithCommandInitializer(ctx context.Context, cmdInit func(cmd *cobra.Command) error) context.Context {
	return context.WithValue(ctx, cmdInitKey{}, cmdInit)
}

func InitCommand(cmd *cobra.Command) (err error) {
	cmdInit, ok := cmd.Context().Value(cmdInitKey{}).(func(cmd *cobra.Command) error)
	if !ok {
		panic("no registered command initializer")
	}
	return cmdInit(cmd)
}

func CommandInitializer(cmd *cobra.Command) (err error) {
	ctx := cmd.Context()
	as := cmd.Annotations

	if v, ok := as[ann.Session]; ok {
		as[ann.UserDaemon] = v
		as[ann.VersionCheck] = ann.Required
	}
	if v := as[ann.UserDaemon]; v == ann.Optional || v == ann.Required {
		if cr := daemon.GetRequest(ctx); cr == nil {
			if ctx, err = daemon.WithDefaultRequest(ctx, cmd); err != nil {
				return err
			}
			flags.DeprecationIfChanged(cmd, global.FlagDocker, "use telepresence connect to initiate the connection")
			flags.DeprecationIfChanged(cmd, global.FlagContext, "use telepresence connect to initiate the connection")
		}
		if ctx, err = EnsureUserDaemon(ctx, v == ann.Required); err != nil {
			if v == ann.Optional && (err == ErrNoUserDaemon || errcat.GetCategory(err) == errcat.Config) {
				// This is OK, but further initialization is not possible
				err = nil
			}
			return err
		}
		cmd.SetContext(ctx)
	} else {
		// The rest requires a user daemon
		return nil
	}
	if as[ann.VersionCheck] == ann.Required {
		if err = ensureDaemonVersion(ctx); err != nil {
			return err
		}
	}

	if v := as[ann.Session]; v == ann.Optional || v == ann.Required {
		ctx, err = EnsureSession(ctx, cmd.UseLine(), v == ann.Required)
		if err != nil {
			return err
		}
		cmd.SetContext(ctx)
	}
	return nil
}
