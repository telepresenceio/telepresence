package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,

		Short:   "Show version",
		PreRunE: util.ForcedUpdateCheck,
		RunE:    printVersion,
		Annotations: map[string]string{
			ann.RootDaemon:        ann.Optional,
			ann.UserDaemon:        ann.Optional,
			ann.UpdateCheckFormat: ann.Tel2,
		},
	}
}

func printVersion(cmd *cobra.Command, _ []string) error {
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	kvf := ioutil.DefaultKeyValueFormatter()
	kvf.Add(client.DisplayName, client.Version())

	ctx := cmd.Context()
	version, err := daemonVersion(ctx)
	switch {
	case err == nil:
		kvf.Add("Root Daemon", version.Version)
	case err == util.ErrNoRootDaemon:
		kvf.Add("Root Daemon", "not running")
	default:
		kvf.Add("Root Daemon", fmt.Sprintf("error: %v", err))
	}

	version, err = connectorVersion(ctx)
	switch {
	case err == nil:
		kvf.Add("User Daemon", version.Version)
		var mgrVer *manager.VersionInfo2
		mgrVer, err = managerVersion(ctx)
		switch {
		case err == nil:
			kvf.Add("Traffic Manager", mgrVer.Version)
		case status.Code(err) == codes.Unavailable:
			kvf.Add("Traffic Manager", "not connected")
		default:
			kvf.Add("Traffic Manager", fmt.Sprintf("error: %v", err))
		}
	case err == util.ErrNoUserDaemon:
		kvf.Add("User Daemon", "not running")
	default:
		kvf.Add("User Daemon", fmt.Sprintf("error: %v", err))
	}
	out := cmd.OutOrStdout()
	_, _ = kvf.WriteTo(out)
	ioutil.WriteString(out, "\n")
	return nil
}

func daemonVersion(ctx context.Context) (*common.VersionInfo, error) {
	if conn, err := client.DialSocket(ctx, client.DaemonSocketName); err == nil {
		defer conn.Close()
		return daemon.NewDaemonClient(conn).Version(ctx, &empty.Empty{})
	}
	return nil, util.ErrNoRootDaemon
}

func connectorVersion(ctx context.Context) (*common.VersionInfo, error) {
	if userD := util.GetUserDaemon(ctx); userD != nil {
		return userD.Version(ctx, &empty.Empty{})
	}
	return nil, util.ErrNoUserDaemon
}

func managerVersion(ctx context.Context) (*manager.VersionInfo2, error) {
	userD := util.GetUserDaemon(ctx)
	if userD == nil {
		return nil, util.ErrNoUserDaemon
	}
	return manager.NewManagerClient(userD.Conn).Version(ctx, &empty.Empty{})
}
