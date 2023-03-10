package util

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

type userDaemonKey struct{}

func GetUserDaemon(ctx context.Context) *UserDaemon {
	if ud, ok := ctx.Value(userDaemonKey{}).(*UserDaemon); ok {
		return ud
	}
	return nil
}

type sessionKey struct{}

func GetSession(ctx context.Context) *Session {
	if s, ok := ctx.Value(sessionKey{}).(*Session); ok {
		return s
	}
	return nil
}

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
		as[ann.RootDaemon] = v
		as[ann.VersionCheck] = ann.Required
	}
	if v := as[ann.UserDaemon]; v == ann.Optional || v == ann.Required {
		if cr := daemon.GetRequest(ctx); cr == nil {
			ctx = daemon.WithDefaultRequest(ctx, cmd)
		}
		if ctx, err = ensureUserDaemon(ctx, v == ann.Required); err != nil {
			if v == ann.Optional && err == ErrNoUserDaemon {
				// This is OK, but further initialization is not possible
				err = nil
			}
			return err
		}

		// RootDaemon == Optional means that the RootDaemon must be started if
		// the UserDaemon was started
		if _, ok := as[ann.RootDaemon]; ok {
			if err = ensureRootDaemonRunning(ctx); err != nil {
				return err
			}
		}
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
		if ctx, err = ensureSession(ctx, v == ann.Required); err != nil {
			return err
		}
	}
	cmd.SetContext(ctx)
	return nil
}
