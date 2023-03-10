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
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,

		Short:   "Show version",
		PreRunE: cloud.ForcedUpdateCheck,
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

	remote := false
	userD := util.GetUserDaemon(ctx)
	if userD != nil {
		remote = userD.Remote
	}

	if !remote {
		version, err := daemonVersion(ctx)
		switch {
		case err == nil:
			kvf.Add(version.Name, version.Version)
		case err == util.ErrNoRootDaemon:
			kvf.Add("Root Daemon", "not running")
		default:
			kvf.Add("Root Daemon", fmt.Sprintf("error: %v", err))
		}
	}

	if userD != nil {
		version, err := userD.Version(ctx, &empty.Empty{})
		if err == nil {
			kvf.Add(version.Name, version.Version)
			version, err = managerVersion(ctx)
			switch {
			case err == nil:
				kvf.Add(version.Name, version.Version)
			case status.Code(err) == codes.Unavailable:
				kvf.Add("Traffic Manager", "not connected")
			default:
				kvf.Add("Traffic Manager", fmt.Sprintf("error: %v", err))
			}
		} else {
			kvf.Add("User Daemon", fmt.Sprintf("error: %v", err))
		}
	} else {
		kvf.Add("User Daemon", "not running")
	}
	kvf.Println(cmd.OutOrStdout())
	return nil
}

func daemonVersion(ctx context.Context) (*common.VersionInfo, error) {
	if conn, err := socket.Dial(ctx, socket.DaemonName); err == nil {
		defer conn.Close()
		return daemon.NewDaemonClient(conn).Version(ctx, &empty.Empty{})
	}
	return nil, util.ErrNoRootDaemon
}

func managerVersion(ctx context.Context) (*common.VersionInfo, error) {
	if userD := util.GetUserDaemon(ctx); userD != nil {
		return userD.TrafficManagerVersion(ctx, &empty.Empty{})
	}
	return nil, util.ErrNoUserDaemon
}
