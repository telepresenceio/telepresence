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
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	tpGrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,

		Short: "Show version",
		RunE:  printVersion,
		Annotations: map[string]string{
			ann.UserDaemon:        ann.Optional,
			ann.UpdateCheckFormat: ann.Tel2,
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
}

// versionInfo is the structured (--format) representation of `telepresence
// version`. Its keys mirror those used by `telepresence status` (snake_case
// component names) rather than the human-readable display names.
type versionInfo struct {
	Client         string                  `json:"client,omitempty"`
	RootDaemon     string                  `json:"root_daemon,omitempty"`
	UserDaemon     string                  `json:"user_daemon,omitempty"`
	TrafficManager string                  `json:"traffic_manager,omitempty"`
	TrafficAgent   string                  `json:"traffic_agent,omitempty"`
	Connections    map[string]*versionInfo `json:"connections,omitempty"`
}

// versionCollector records each component version into both the human-readable
// KeyValueFormatter (using display names) and the structured versionInfo (using
// status-style snake_case keys) in a single traversal.
type versionCollector struct {
	kvf *ioutil.KeyValueFormatter
	vi  *versionInfo
}

func newVersionCollector() *versionCollector {
	return &versionCollector{kvf: ioutil.DefaultKeyValueFormatter(), vi: &versionInfo{}}
}

func newSubVersionCollector(parent *versionCollector) *versionCollector {
	return &versionCollector{
		kvf: &ioutil.KeyValueFormatter{Indent: parent.kvf.Indent, Separator: parent.kvf.Separator},
		vi:  &versionInfo{},
	}
}

// set records value under displayName for the text output and into the given
// structured field for the JSON/YAML output.
func (c *versionCollector) set(field *string, displayName, value string) {
	c.kvf.Add(displayName, value)
	*field = value
}

func addDaemonVersions(ctx context.Context, c *versionCollector) {
	hasRootDaemon := true
	userD := daemon.GetUserClient(ctx)
	if userD != nil {
		hasRootDaemon = !(proc.IsAdmin() || userD.Containerized())
	}

	if hasRootDaemon {
		version, err := daemonVersion(ctx)
		switch {
		case err == nil:
			c.set(&c.vi.RootDaemon, version.Name, version.Version)
		case errors.Is(err, daemon.ErrNoRootDaemon):
			c.set(&c.vi.RootDaemon, "Root Daemon", "not running")
		default:
			c.set(&c.vi.RootDaemon, "Root Daemon", fmt.Sprintf("error: %v", err))
		}
	}

	if userD != nil {
		c.set(&c.vi.UserDaemon, userD.Name(), "v"+userD.Semver().String())
		vi, err := managerVersion(ctx)
		switch {
		case err == nil:
			c.set(&c.vi.TrafficManager, vi.Name, vi.Version)
			af, err := trafficAgentFQN(ctx)
			switch status.Code(err) {
			case codes.OK:
				c.set(&c.vi.TrafficAgent, "Traffic Agent", af.FQN)
			case codes.Unimplemented:
				c.set(&c.vi.TrafficAgent, "Traffic Agent", "not reported by traffic-manager")
			case codes.Unavailable:
				c.set(&c.vi.TrafficAgent, "Traffic Agent", "not currently available")
			default:
				c.set(&c.vi.TrafficAgent, "Traffic Agent", fmt.Sprintf("error: %v", err))
			}
		case status.Code(err) == codes.Unavailable:
			c.set(&c.vi.TrafficManager, "Traffic Manager", "not connected")
		default:
			c.set(&c.vi.TrafficManager, "Traffic Manager", fmt.Sprintf("error: %v", err))
		}
	} else {
		c.set(&c.vi.UserDaemon, "User Daemon", "not running")
	}
}

func printVersion(cmd *cobra.Command, _ []string) error {
	c := newVersionCollector()
	c.set(&c.vi.Client, client.DisplayName, client.Version())

	var mdErr daemon.MultipleDaemonsError
	err := connect.InitCommand(cmd)
	if err != nil {
		if !errors.As(err, &mdErr) {
			return err
		}
	}
	defer progress.Stop(cmd.Context())
	ctx := cmd.Context()

	if len(mdErr) > 0 {
		c.vi.Connections = make(map[string]*versionInfo, len(mdErr))
		for _, info := range mdErr {
			sub := newSubVersionCollector(c)
			udCtx, err := connect.ExistingDaemon(ctx, info)
			if err != nil {
				sub.set(&sub.vi.UserDaemon, "User Daemon", fmt.Sprintf("error: %v", err))
			}
			addDaemonVersions(udCtx, sub)
			ud := daemon.MustGetUserClient(udCtx)
			name := ud.DaemonID().Name
			c.kvf.Add("Connection "+name, "\n"+sub.kvf.String())
			c.vi.Connections[name] = sub.vi
			_ = ud.Close()
		}
	} else {
		addDaemonVersions(ctx, c)
	}

	if output.WantsClean(cmd) {
		// --format: emit the structured object. The deprecated --output keeps the
		// historical {cmd, stdout: "<text>"} shape via the text path below.
		output.Object(ctx, c.vi, true)
	} else {
		c.kvf.Println(cmd.OutOrStdout())
	}
	return nil
}

func daemonVersion(ctx context.Context) (*common.VersionInfo, error) {
	if conn, err := daemon.DialRootDaemon(ctx, false); err == nil {
		defer conn.Close()
		return daemonRpc.NewDaemonClient(conn).Version(ctx, &empty.Empty{})
	}
	return nil, daemon.ErrNoRootDaemon
}

func managerVersion(ctx context.Context) (*common.VersionInfo, error) {
	if s := daemon.GetSession(ctx); s != nil {
		mv := s.Info.ManagerVersion
		return &common.VersionInfo{
			Version: mv.Version,
			Name:    mv.Name,
		}, nil
	}
	if userD := daemon.GetUserClient(ctx); userD != nil {
		mv, err := userD.TrafficManagerVersion(ctx, &empty.Empty{})
		return mv, tpGrpc.FromGRPC(err)
	}
	return nil, daemon.ErrNoUserDaemon
}

func trafficAgentFQN(ctx context.Context) (*manager.AgentImageFQN, error) {
	if userD := daemon.GetUserClient(ctx); userD != nil {
		ai, err := userD.AgentImageFQN(ctx, &empty.Empty{})
		return ai, tpGrpc.FromGRPC(err)
	}
	return nil, daemon.ErrNoUserDaemon
}
