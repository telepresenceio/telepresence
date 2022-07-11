package userd

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func GetCommands(ctx context.Context) cliutil.CommandGroups {
	var s service
	return s._getCommands(ctx)
}

func GetCommandsForLocal(ctx context.Context, err error) cliutil.CommandGroups {
	groups := GetCommands(ctx)
	for _, cmds := range groups {
		for _, cmd := range cmds {
			cmd.RunE = func(_ *cobra.Command, _ []string) error {
				// err here will be ErrNoUserDaemon "telepresence user daemon is not running"
				return fmt.Errorf("unable to run command: %w", err)
			}
		}
	}
	return groups
}

// GetCommandsForLocal will return the same commands as GetCommands but in a non-runnable state that reports
// the error given. Should be used to build help strings even if it's not possible to connect to the connector daemon.
func (s *service) GetCommandsForLocal(ctx context.Context, err error) cliutil.CommandGroups {
	groups := s._getCommands(ctx)
	for _, cmds := range groups {
		for _, cmd := range cmds {
			cmd.RunE = func(_ *cobra.Command, _ []string) error {
				// err here will be ErrNoUserDaemon "telepresence user daemon is not running"
				return fmt.Errorf("unable to run command: %w", err)
			}
		}
	}
	return groups
}

// GetCommands will return all commands implemented by the connector daemon.
func (s *service) _getCommands(ctx context.Context) cliutil.CommandGroups {
	var (
		st = reflect.TypeOf(s)
		sv = reflect.ValueOf(s)

		ctxv = reflect.ValueOf(ctx)
		cg   = cliutil.CommandGroups{}
	)

	for i := 0; i < st.NumMethod(); i++ {
		m := st.Method(i)
		if !strings.HasPrefix(m.Name, "_cmd") {
			continue
		}
		cmdv := m.Func.Call([]reflect.Value{sv, ctxv})[0]
		cmd := cmdv.Interface().(*cobra.Command)
		annotations := cmd.Annotations
		if group, ok := annotations["cobra.commandGroup"]; ok {
			cmds := cg[group]
			if cmds == nil {
				cmds = []*cobra.Command{}
			}
			cmds = append(cmds, cmd)
			cg[group] = cmds
		}
	}

	return cg
}
