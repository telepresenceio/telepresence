package cmd

import "github.com/spf13/cobra"

// leaveCmd is a deprecated alias for detachCmd, kept for backward compatibility.
func leaveCmd() *cobra.Command {
	cmd := newDetachCmd("leave")
	cmd.Hidden = true
	cmd.Deprecated = `use "telepresence detach" instead`
	return cmd
}
