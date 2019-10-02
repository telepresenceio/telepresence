package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/kubeapply"
)

func envBool(name string) bool {
	val, _ := strconv.ParseBool(name)
	return val
}

// Version holds the version of the code. This is intended to be overridden at build time.
var Version = "(unknown version)"

func main() {
	var ka = &cobra.Command{
		Use:           "kubeapply",
		Short:         "kubeapply",
		Long:          "kubeapply - the way kubectl aught to work",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	kubeconfig := ka.Flags().String("kubeconfig", "", "kubernetes config file")
	context := ka.Flags().String("context", "", "kubernetes context")
	namespace := ka.Flags().StringP("namespace", "n", "", "kubernetes namespace")
	debug := ka.Flags().Bool("debug", envBool("KUBEAPPLY_DEBUG"), "enable debug mode")
	dryRun := ka.Flags().Bool("dry-run", envBool("KUBEAPPLY_DRYRUN"), "enable dry-run mode")
	timeout := ka.Flags().DurationP("timeout", "t", time.Minute,
		"timeout to wait for each applied YAML phase to become ready")
	showVersion := ka.Flags().Bool("version", false, "output version information and exit")
	files := ka.Flags().StringSliceP("filename", "f", nil, "files to apply")

	ka.RunE = func(cmd *cobra.Command, args []string) error {
		if *showVersion {
			fmt.Println(Version)
			return nil
		}
		if len(args) > 0 {
			return errors.Errorf("extra args: %v", args)
		}
		if len(*files) == 0 {
			return errors.Errorf("at least one file argument is required")
		}
		return kubeapply.Kubeapply(k8s.NewKubeInfo(*kubeconfig, *context, *namespace), *timeout,
			*debug, *dryRun, *files...)
	}

	err := ka.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
