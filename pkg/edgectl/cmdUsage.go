package edgectl

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// CmdGroup represents a group of commands and the name of that group
type CmdGroup struct {
	GroupName string
	CmdNames  []string
}

// NewCmdUsage constructs a cobra UsageFunc for a command based on the command
// groups passed in. Requires that the command has exactly the subcommands
// mentioned in the groups, panicking otherwise.
func NewCmdUsage(cmd *cobra.Command, groups []CmdGroup) func(*cobra.Command) error {
	origUF := cmd.UsageFunc()
	cmdMap := make(map[string]*cobra.Command)
	for _, subCmd := range cmd.Commands() {
		if subCmd.IsAvailableCommand() || subCmd.Name() == "help" {
			cmdMap[subCmd.Name()] = subCmd
		}
	}
	lines := []string{}
	for _, cmdGroup := range groups {
		lines = append(lines, fmt.Sprintf("%s:", cmdGroup.GroupName))
		for _, cmdName := range cmdGroup.CmdNames {
			cmd, ok := cmdMap[cmdName]
			if !ok {
				panic(fmt.Sprintf("Unknown command %q in group %q", cmdName, cmdGroup.GroupName))
			}
			delete(cmdMap, cmdName)
			line := fmt.Sprintf("  %s %s", rpad(cmd.Name(), cmd.NamePadding()), cmd.Short)
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}
	usage := strings.Join(lines, "\n")

	if len(cmdMap) != 0 {
		panic(fmt.Sprintf("CmdUsage is not empty: %+v", cmdMap))
	}

	return func(cmd *cobra.Command) error {
		origOutput := cmd.OutOrStderr()
		var buf bytes.Buffer
		cmd.SetOutput(&buf)
		if err := origUF(cmd); err != nil {
			return err
		}
		scanner := bufio.NewScanner(strings.NewReader(buf.String()))
		state := 0 // States described in switch statement cases
		for scanner.Scan() {
			line := scanner.Text()
			switch state {
			case 0:
				// Before the original command list. Pass through until we see
				// the start of the original command list, at which point we go
				// to state 1.
				if line == "Available Commands:" {
					state = 1
					fmt.Fprintln(origOutput, usage)
				} else {
					fmt.Fprintln(origOutput, line)
				}
			case 1:
				// Skipping past the original command list, which should end
				// with a blank line. We've already output our own command list
				// at the state transition, so do nothing else.
				if line == "" {
					state = 2
				}
			case 2:
				// Done skipping past the original command list. Just pass
				// through the rest.
				fmt.Fprintln(origOutput, line)
			default:
				panic(fmt.Sprintf("Unknown state %d", state))
			}
		}
		return scanner.Err()
	}
}

func rpad(s string, padding int) string {
	template := fmt.Sprintf("%%-%ds", padding)
	return fmt.Sprintf(template, s)
}
