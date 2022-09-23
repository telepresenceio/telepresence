package cliutil

import (
	"context"
	"fmt"

	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func versionCheck(ctx context.Context, daemonBinary string, configuredDaemon bool, daemon connector.ConnectorClient) error {
	if IsQuitting(ctx) {
		return nil
	}
	vr, err := daemon.RootDaemonVersion(ctx, &empty.Empty{})
	if err == nil && version.Version != vr.Version {
		return errcat.User.Newf("version mismatch. Client %s != Root Daemon %s, please run 'telepresence quit -s' and reconnect",
			version.Version, vr.Version)
	}

	// Ensure that the already running daemons have the correct version
	vu, err := daemon.Version(ctx, &empty.Empty{})
	if err != nil {
		return fmt.Errorf("unable to retrieve version of User Daemon: %w", err)
	}
	if version.Version != vu.Version {
		// OSS Version mismatch. We never allow this
		if !configuredDaemon {
			return errcat.User.Newf("version mismatch. Client %s != User Daemon %s, please run 'telepresence quit -s' and reconnect",
				version.Version, vu.Version)
		}
		return GetTelepresencePro(ctx)
	}
	if daemonBinary != "" {
		if vu.Executable != daemonBinary {
			return errcat.User.Newf("executable mismatch. Connector using %s, configured to use %s, please run 'telepresence quit -s' and reconnect",
				vu.Executable, daemonBinary)
		}
	}
	return nil
}
