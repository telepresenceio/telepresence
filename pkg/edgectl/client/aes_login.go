package client

import (
	"fmt"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/gookit/color"
	"github.com/spf13/cobra"
)

func AESLogin(cmd *cobra.Command, args []string) error {
	fmt.Println(color.Info.Sprintf("Connecting to the Ambassador Edge Policy Console in this cluster..."))

	// Grab options
	context, _ := cmd.Flags().GetString("context")
	namespace, _ := cmd.Flags().GetString("namespace")
	justShowURL, _ := cmd.Flags().GetBool("url")
	showToken, _ := cmd.Flags().GetBool("token")

	// Figure out the correct hostname
	hostname := args[0]

	// Prepare to talk to the cluster
	kubeinfo := k8s.NewKubeInfo("", context, namespace) // Default namespace is "ambassador"

	return edgectl.DoLogin(kubeinfo, context, namespace, hostname, !justShowURL, justShowURL, showToken, false)
}
