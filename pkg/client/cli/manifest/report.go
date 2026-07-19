package manifest

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type summary struct {
	Connection  string             `json:"connection,omitempty"`
	Attachments []attachmentResult `json:"attachments,omitempty"`
}

// printSummary prints the outcome of apply/delete, honoring --output/--format like other
// commands: a single structured object when formatted output was requested, otherwise one line
// per attachment (plus an optional connection line).
func printSummary(cmd *cobra.Command, connLine string, results []attachmentResult) {
	ctx := cmd.Context()
	if output.WantsFormatted(cmd) {
		output.Object(ctx, summary{Connection: connLine, Attachments: results}, true)
		return
	}
	out := output.Out(ctx)
	if connLine != "" {
		ioutil.Printf(out, "connection: %s\n", connLine)
	}
	for _, r := range results {
		line := fmt.Sprintf("%s %s: %s", r.Type, r.Name, r.Action)
		if r.Detail != "" {
			line += fmt.Sprintf(" (drift: %s)", r.Detail)
		}
		if r.Handler != "" {
			line += fmt.Sprintf(", handler: %s", r.Handler)
		}
		ioutil.Printf(out, "%s\n", line)
	}
}
