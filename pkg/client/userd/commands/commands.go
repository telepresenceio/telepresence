package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

const (
	CommandRequiresSession                        = "cobra.telepresence.io/with-session"
	CommandRequiresConnectorServer                = "cobra.telepresence.io/with-connector-server"
	ValidArgsFuncRequiresConnectorServer          = "cobra.telepresence.io/valid-args-func/with-connector-server"
	FlagAutocompletionFuncRequiresConnectorServer = "cobra.telepresence.io/flag-autocompletion-func/with-connector-server"
	FlagAutocompletionFuncRequiresSession         = "cobra.telepresence.io/flag-autocompletion-func/with-session"
)

type command interface {
	init(context.Context)
	cobraCommand(context.Context) *cobra.Command
	group() string
}

type argAutocompleter interface {
	validArgsFunc() AutocompletionFunc
}

type flagAutocompleter interface {
	flagAutocompletionFunc(flagName string) AutocompletionFunc
}

type AutocompletionFunc func(ctx context.Context, cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective)

func commands() []command {
	return []command{
		&interceptCommand{},
		&traceCommand{},
		&pushTracesCommand{},
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
		cmd.init(ctx)
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

func GetCommandByName(ctx context.Context, name string) *cobra.Command {
	for _, cmd := range commands() {
		if cmd.cobraCommand(ctx).Name() == name {
			cmd.init(ctx)
			return cmd.cobraCommand(ctx)
		}
	}

	return nil
}

func GetValidArgsFunctionFor(ctx context.Context, cmd *cobra.Command) AutocompletionFunc {
	name := cmd.Name()
	for _, cmd := range commands() {
		if cmd.cobraCommand(ctx).Name() != name {
			continue
		}
		if ac, ok := cmd.(argAutocompleter); ok {
			return ac.validArgsFunc()
		} else {
			return nil
		}
	}
	return nil
}

func GetFlagAutocompletionFuncFor(ctx context.Context, cmd *cobra.Command, flagName string) AutocompletionFunc {
	name := cmd.Name()
	for _, cmd := range commands() {
		if cmd.cobraCommand(ctx).Name() != name {
			continue
		}
		if ac, ok := cmd.(flagAutocompleter); ok {
			return ac.flagAutocompletionFunc(flagName)
		} else {
			return nil
		}
	}
	return nil
}

type connectorKey struct{}

type ConnectorServer interface {
	rpc.ConnectorServer
	FuseFTPError() error
}

func WithConnectorServer(ctx context.Context, cs ConnectorServer) context.Context {
	return context.WithValue(ctx, connectorKey{}, cs)
}

func GetConnectorServer(ctx context.Context) ConnectorServer {
	cs, _ := ctx.Value(connectorKey{}).(ConnectorServer)
	return cs
}

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
