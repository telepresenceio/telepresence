package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
)

const (
	CommandRequiresSession         = "cobra.telepresence.io/with-session"
	CommandRequiresConnectorServer = "cobra.telepresence.io/with-connector-server"
)

type cwdKey struct{}

func WithCwd(ctx context.Context, cwd string) context.Context {
	return context.WithValue(ctx, cwdKey{}, cwd)
}

func GetCwd(ctx context.Context) string {
	if wd, ok := ctx.Value(cwdKey{}).(string); ok {
		return wd
	}
	return ""
}

type command interface {
	init(context.Context)
	cobraCommand(context.Context) *cobra.Command
	group() string
}

func commands() []command {
	return []command{
		&interceptCommand{},
	}
}

// GetCommands will return all commands implemented by the connector daemon.
func GetCommands(ctx context.Context) cliutil.CommandGroups {
	var groups = cliutil.CommandGroups{}
	for _, cmd := range commands() {
		var (
			groupName = cmd.group()
			group     = groups[groupName]
		)
		groups[groupName] = append(group, cmd.cobraCommand(ctx))
	}
	return groups
}

// GetCommandsForLocal will return the same commands as GetCommands but in a non-runnable state that reports
// the error given. Should be used to build help strings even if it's not possible to connect to the connector daemon.
func GetCommandsForLocal(ctx context.Context, err error) cliutil.CommandGroups {
	var groups = cliutil.CommandGroups{}
	for _, cmd := range commands() {
		var (
			groupName = cmd.group()
			group     = groups[groupName]
			cc        = cmd.cobraCommand(ctx)
		)
		cc.RunE = func(_ *cobra.Command, _ []string) error {
			// err here will be ErrNoUserDaemon "telepresence user daemon is not running"
			return fmt.Errorf("unable to run command: %w", err)
		}
		groups[groupName] = append(group, cc)
	}
	return groups
}

type sessKey struct{}

func WithSession(ctx context.Context, s trafficmgr.Session) context.Context {
	return context.WithValue(ctx, sessKey{}, s)
}

func getSession(ctx context.Context) trafficmgr.Session {
	s, _ := ctx.Value(sessKey{}).(trafficmgr.Session)
	return s
}

type connectorKey struct{}

func WithConnectorServer(ctx context.Context, cs connector.ConnectorServer) context.Context {
	return context.WithValue(ctx, connectorKey{}, cs)
}

func getConnectorServer(ctx context.Context) connector.ConnectorServer {
	cs, _ := ctx.Value(connectorKey{}).(connector.ConnectorServer)
	return cs
}
