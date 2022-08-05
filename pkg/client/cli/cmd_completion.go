package cli

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func addCompletionCommand(rootCmd *cobra.Command) {
	cmd := cobra.Command{
		Use:   "completion",
		Short: "generate a shell completion script",
		ValidArgs: []string{
			"bash",
			"zsh",
			"powershell",
			"fish",
		},
		ArgAliases: []string{"ps"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var shell string
			if 0 < len(args) {
				shell = args[0]
			}

			var err error
			switch shell {
			case "zsh":
				err = rootCmd.GenZshCompletionNoDesc(os.Stdout)
			case "bash":
				err = rootCmd.GenBashCompletionV2(os.Stdout, false)
			case "fish":
				err = rootCmd.GenFishCompletion(os.Stdout, false)
			case "ps", "powershell":
				err = rootCmd.GenPowerShellCompletion(os.Stdout)
			case "":
				err = errcat.User.Newf("shell not specified")
			}

			return err
		},
	}

	rootCmd.AddCommand(&cmd)
}
