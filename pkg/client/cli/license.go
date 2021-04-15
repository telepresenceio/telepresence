package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

type licenseInfo struct {
	outputFile string
}

func LicenseCommand() *cobra.Command {
	li := &licenseInfo{}
	cmd := &cobra.Command{
		Use:  "license",
		Args: cobra.MinimumNArgs(1),

		Short: "Get License from Ambassador Cloud",
		Long:  "Get License from Ambasssador Cloud",
		RunE:  li.getCloudLicense,
	}
	flags := cmd.Flags()

	flags.StringVarP(&li.outputFile, "output-file", "f", "/tmp/license", "The file where you want the license secret to be output to")
	return cmd
}

func (li *licenseInfo) getCloudLicense(cmd *cobra.Command, args []string) error {
	outputFile := strings.TrimSpace(li.outputFile)
	id := strings.TrimSpace(args[0])
	_, err := cliutil.GetCloudLicense(cmd.Context(), outputFile, id)
	if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "License added to secret and written to: %s", outputFile)
	}
	return err
}
