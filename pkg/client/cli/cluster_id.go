package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dexec"
)

// getClusterID is a simple command that makes it easier for users to
// figure out what their cluster ID is. For now this is just used when
// people are making licenses for air-gapped environments
func ClusterIdCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "current-cluster-id",
		Args: cobra.NoArgs,

		Short: "Get cluster ID for your kubernetes cluster",
		Long:  "Get cluster ID for your kubernetes cluster, mostly used for licenses in air-gapped environments",
		RunE: func(flags *cobra.Command, _ []string) error {
			// NB: Even without logging, dexec is still an improvement over os/exec
			// because it better handles kubectl hanging.
			cmd := dexec.CommandContext(flags.Context(), "kubectl", "get", "ns", "default", "-o", "jsonpath={.metadata.uid}")
			cmd.DisableLogging = true
			cmd.Stderr = flags.ErrOrStderr()

			output, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("kubectl: %w", err)
			}
			fmt.Fprintf(flags.OutOrStdout(), "Cluster ID: %s", output)
			return nil
		},
	}
}
