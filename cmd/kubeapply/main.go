package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/datawire/teleproxy/pkg/kubeapply"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func envBool(name string) bool {
	val, _ := strconv.ParseBool(name)
	return val
}

// Version holds the version of the code. This is intended to be overridden at build time.
var Version = "(unknown version)"

func adapt(f func(*cobra.Command, []string) error) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		err := f(cmd, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func main() {
	var ka = &cobra.Command{
		Use:   "kubeapply",
		Short: "kubeapply",
		Long:  "kubeapply - the way kubectl aught to work",
	}

	debug := ka.Flags().Bool("debug", envBool("KUBEAPPLY_DEBUG"), "enable debug mode")
	dryRun := ka.Flags().Bool("dry-run", envBool("KUBEAPPLY_DRYRUN"), "enable dry-run mode")
	timeout := ka.Flags().DurationP("timeout", "t", 60*time.Second, "timeout to wait for applied yaml to be ready")
	showVersion := ka.Flags().Bool("version", false, "output version information and exit")
	files := ka.Flags().StringArrayP("", "f", nil, "files to apply")

	ka.Run = adapt(func(cmd *cobra.Command, args []string) error {
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
		return kubeapply.Kubeapply(nil, *timeout, *debug, *dryRun, *files...)
	})

	err := ka.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
