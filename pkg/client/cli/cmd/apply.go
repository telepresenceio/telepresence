package cmd

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/manifest"
)

func applyCmd() *cobra.Command {
	var file string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply -f <manifest>",
		Args:  cobra.NoArgs,
		Short: "Bring the workstation to the state described by a manifest",
		Long: `Bring the workstation to the state described by a manifest: a declarative
YAML description of a Telepresence connection and its attachments (intercept,
replace, ingest, or wiretap). Attachments are established in manifest order;
attachments already present with a matching spec are left untouched, and
attachments whose spec has drifted are re-created.`,
		Annotations: map[string]string{
			ann.UpdateCheckFormat: ann.Tel2,
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := manifest.LoadFile(file)
			if err != nil {
				return err
			}
			return manifest.Apply(cmd, st, dryRun)
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&file, "filename", "f", "", `The manifest file to apply. Use "-" to read from stdin`)
	_ = cmd.MarkFlagRequired("filename")
	_ = cmd.MarkFlagFilename("filename")
	flags.BoolVar(&dryRun, "dry-run", false, "Report what apply would do, without changing anything")
	return cmd
}
