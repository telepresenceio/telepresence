package main

import (
	"fmt"
	"os"

	"github.com/datawire/teleproxy/pkg/teleproxy"
	"github.com/spf13/cobra"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

func main() {
	args := teleproxy.Teleproxy{}

	var tp = &cobra.Command{
		Use:           "teleproxy",
		Short:         "teleproxy",
		Long:          "teleproxy - connect locally running code to a remote kubernetes cluster",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	tp.Flags().BoolVar(&args.Version, "version", false, "alias for '-mode=version'")
	tp.Flags().StringVar(&args.Mode, "mode", "", "mode of operation ('intercept', 'bridge', or 'version')")
	tp.Flags().StringVar(&args.Kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	tp.Flags().StringVar(&args.Context, "context", "", "context to use (default: the current context)")
	tp.Flags().StringVar(&args.Namespace, "namespace", "",
		"namespace to use (default: the current namespace for the context")
	tp.Flags().StringVar(&args.DNSIP, "dns", "", "dns ip address")
	tp.Flags().StringVar(&args.FallbackIP, "fallback", "", "dns fallback")
	tp.Flags().BoolVar(&args.NoSearch, "no-search-override", false, "disable dns search override")
	tp.Flags().BoolVar(&args.NoCheck, "no-check", false, "disable self check")

	tp.RunE = func(cmd *cobra.Command, _ []string) error {
		return teleproxy.RunTeleproxy(args, Version)
	}

	err := tp.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
