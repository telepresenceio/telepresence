package cliutil

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func versionCheck(ctx context.Context, daemonType string, daemonBinary string, client interface {
	Version(context.Context, *empty.Empty, ...grpc.CallOption) (*common.VersionInfo, error)
}) error {
	if ctx.Value(quitting{}) != nil {
		return nil
	}
	// Ensure that the already running daemon has the correct version
	vi, err := client.Version(ctx, &empty.Empty{})
	if err != nil {
		return fmt.Errorf("unable to retrieve version of %s Daemon: %w", daemonType, err)
	}
	if version.Version != vi.Version {
		return errcat.User.Newf("version mismatch. Client %s != %s Daemon %s, please quit telepresence and reconnect", version.Version, daemonType, vi.Version)
	}
	if daemonBinary != "" {
		if vi.Executable != daemonBinary {
			return errcat.User.Newf("executable mismatch. Connector using %s, configured to use %s, please quit telepresence and reconnect", vi.Executable, daemonBinary)
		}
	}
	return nil
}
