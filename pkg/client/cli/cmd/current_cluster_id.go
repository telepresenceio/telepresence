package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/k8sapi/pkg/k8sapi"
)

// currentClusterId is a simple command that makes it easier for users to
// figure out what their cluster ID is. For now this is just used when
// people are making licenses for air-gapped environments.
func currentClusterId() *cobra.Command {
	kubeConfig := genericclioptions.NewConfigFlags(false)
	cmd := &cobra.Command{
		Use:  "current-cluster-id",
		Args: cobra.NoArgs,

		Short: "Get cluster ID for your kubernetes cluster",
		Long:  "Get cluster ID for your kubernetes cluster, mostly used for licenses in air-gapped environments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			restConfig, err := kubeConfig.ToRESTConfig()
			if err != nil {
				return err
			}
			ki, err := kubernetes.NewForConfig(restConfig)
			if err != nil {
				return err
			}
			clusterID, err := k8sapi.GetClusterID(k8sapi.WithK8sInterface(cmd.Context(), ki))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Cluster ID: %s\n", clusterID)
			return nil
		},
	}
	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig.AddFlags(kubeFlags)
	cmd.Flags().AddFlagSet(kubeFlags)
	return cmd
}
