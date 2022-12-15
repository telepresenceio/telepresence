package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func GatherTracesCommand() *cobra.Command {
	tr := connector.TracesRequest{}
	cmd := &cobra.Command{
		Use:  "gather-traces",
		Args: cobra.NoArgs,

		Short: "Gather Traces",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return gatherTraces(cmd, &tr)
		},
		Annotations: map[string]string{
			ann.RootDaemon: ann.Required,
			ann.UserDaemon: ann.Required,
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().Int32VarP(&tr.RemotePort, "port", "p", 15766,
		"The remote port where traffic manager and agent are exposing traces."+
			"Corresponds to tracing.grpcPort in the helm chart values")
	cmd.Flags().StringVarP(&tr.TracingFile, "output-file", "o", "./traces.gz", "The gzip to be created with binary trace data")

	return cmd
}

func gatherTraces(cmd *cobra.Command, request *connector.TracesRequest) error {
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	r, err := util.GetUserDaemon(ctx).GatherTraces(ctx, request)
	if err != nil {
		return err
	}
	if err = errcat.FromResult(r); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Traces saved as %s\n", request.TracingFile)
	return nil
}
