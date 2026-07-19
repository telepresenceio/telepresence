package cmd

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/manifest"
)

func deleteCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "delete -f <manifest>",
		Args:  cobra.NoArgs,
		Short: "Tear down the state described by a manifest",
		Long: `Tear down the state described by a manifest: a declarative YAML description
of a Telepresence connection and its attachments (intercept, replace,
ingest, or wiretap). Attachments are removed in reverse manifest order; an
attachment that's already absent is reported but is not an error. The
connection is only disconnected if the manifest declares one.`,
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
			return manifest.Delete(cmd, st)
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&file, "filename", "f", "", `The manifest file to delete. Use "-" to read from stdin`)
	_ = cmd.MarkFlagRequired("filename")
	_ = cmd.MarkFlagFilename("filename")
	return cmd
}
