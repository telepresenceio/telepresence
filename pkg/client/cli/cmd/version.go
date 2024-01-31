package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	daemonRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func version() *cobra.Command {
	return &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,

		Short: "Show version",
		RunE:  printVersion,
		Annotations: map[string]string{
			ann.UserDaemon:        ann.Optional,
			ann.UpdateCheckFormat: ann.Tel2,
		},
	}
}

func addDaemonVersions(ctx context.Context, kvf *ioutil.KeyValueFormatter) {
	remote := false
	userD := daemon.GetUserClient(ctx)
	if userD != nil {
		remote = userD.Containerized()
	}

	if !remote {
		version, err := daemonVersion(ctx)
		switch {
		case err == nil:
			kvf.Add(version.Name, version.Version)
		case err == connect.ErrNoRootDaemon:
			kvf.Add("Root Daemon", "not running")
		default:
			kvf.Add("Root Daemon", fmt.Sprintf("error: %v", err))
		}
	}

	if userD != nil {
		vi, err := userD.Version(ctx, &empty.Empty{})
		if err == nil {
			kvf.Add(vi.Name, vi.Version)
			vi, err = managerVersion(ctx)
			switch {
			case err == nil:
				kvf.Add(vi.Name, vi.Version)
				af, err := trafficAgentFQN(ctx)
				switch status.Code(err) {
				case codes.OK:
					kvf.Add("Traffic Agent", af.FQN)
				case codes.Unimplemented:
					kvf.Add("Traffic Agent", "not reported by traffic-manager")
				case codes.Unavailable:
					kvf.Add("Traffic Agent", "not currently available")
				default:
					kvf.Add("Traffic Agent", fmt.Sprintf("error: %v", err))
				}
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
}

func printVersion(cmd *cobra.Command, _ []string) error {
	kvf := ioutil.DefaultKeyValueFormatter()
	kvf.Add(client.DisplayName, client.Version())

	var mdErr daemon.MultipleDaemonsError
	err := connect.InitCommand(cmd)
	if err != nil {
		if !errors.As(err, &mdErr) {
			return err
		}
	}
	ctx := cmd.Context()

	if len(mdErr) > 0 {
		for _, info := range mdErr {
			subKvf := &ioutil.KeyValueFormatter{
				Indent:    kvf.Indent,
				Separator: kvf.Separator,
			}
			ud, err := connect.ExistingDaemon(ctx, info)
			if err != nil {
				subKvf.Add("User Daemon", fmt.Sprintf("error: %v", err))
			}
			addDaemonVersions(daemon.WithUserClient(ctx, ud), subKvf)
			kvf.Add("Connection "+ud.DaemonID.Name, "\n"+subKvf.String())
			ud.Conn.Close()
		}
	} else {
		addDaemonVersions(ctx, kvf)
	}

	kvf.Println(cmd.OutOrStdout())
	return nil
}

func daemonVersion(ctx context.Context) (*common.VersionInfo, error) {
	if conn, err := socket.Dial(ctx, socket.RootDaemonPath(ctx)); err == nil {
		defer conn.Close()
		return daemonRpc.NewDaemonClient(conn).Version(ctx, &empty.Empty{})
	}
	return nil, connect.ErrNoRootDaemon
}

func managerVersion(ctx context.Context) (*common.VersionInfo, error) {
	if userD := daemon.GetUserClient(ctx); userD != nil {
		return userD.TrafficManagerVersion(ctx, &empty.Empty{})
	}
	return nil, connect.ErrNoUserDaemon
}

func trafficAgentFQN(ctx context.Context) (*manager.AgentImageFQN, error) {
	if userD := daemon.GetUserClient(ctx); userD != nil {
		return userD.AgentImageFQN(ctx, &empty.Empty{})
	}
	return nil, connect.ErrNoUserDaemon
}
