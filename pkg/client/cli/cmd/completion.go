package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func addCompletion(rootCmd *cobra.Command) {
	cmd := cobra.Command{
		Use:   "completion",
		Short: "Generate a shell completion script",
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
		Long: fmt.Sprintf(`To load completions:

Bash:

  $ source <(%[1]s completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ %[1]s completion bash > /etc/bash_completion.d/%[1]s
  # macOS:
  $ %[1]s completion bash > $(brew --prefix)/etc/bash_completion.d/%[1]s

Zsh:

  # If shell completion is not already enabled in your environment,
  # you will need to enable it.  You can execute the following once:

  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ %[1]s completion zsh > "${fpath[1]}/_%[1]s"

  # You will need to start a new shell for this setup to take effect.

fish:

  $ %[1]s completion fish | source

  # To load completions for each session, execute once:
  $ %[1]s completion fish > ~/.config/fish/completions/%[1]s.fish

PowerShell:

  PS> %[1]s completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS> %[1]s completion powershell > %[1]s.ps1
  # and source this file from your PowerShell profile.
`, rootCmd.Name()),
	}

	rootCmd.AddCommand(&cmd)
}
