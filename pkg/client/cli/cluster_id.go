package cli

import (
	"context"
	"fmt"
	"io"

	// nolint:depguard // This is just a simple kubectl command and we don't want the
	// extra logging that comes with dexec, so I think os.exec should be fine here
	"os/exec"

	"github.com/spf13/cobra"
)

func ClusterIdCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "current-cluster-id",
		Args: cobra.NoArgs,

		Short: "Get cluster ID for your kubernetes cluster",
		Long:  "Get cluster ID for your kubernetes cluster, mostly used for licenses in air-gapped environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getClusterID(context.Background(), cmd.OutOrStdout())
		},
	}

	return cmd
}

// getClusterID is a simple command that makes it easier for users to
// figure out what their cluster ID is. For now this is just used when
// people are making licenses for air-gapped environments
func getClusterID(ctx context.Context, stdout io.Writer) error {
	output, err := exec.Command("kubectl", "get", "ns", "default", "-o", "jsonpath={.metadata.uid}").Output()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Cluster ID: %s", output)
	return nil
}
