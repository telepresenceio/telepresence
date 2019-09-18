package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/datawire/teleproxy/pkg/teleproxy"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

func main() {
	tele := &teleproxy.Teleproxy{}

	var tp = &cobra.Command{
		Use:           "teleproxy",
		Short:         "teleproxy",
		Long:          "teleproxy - connect locally running code to a remote kubernetes cluster",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	tp.Flags().BoolVar(&tele.Version, "version", false, "alias for '-mode=version'")
	tp.Flags().StringVar(&tele.Mode, "mode", "", "mode of operation ('intercept', 'bridge', or 'version')")
	tp.Flags().StringVar(&tele.Kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	tp.Flags().StringVar(&tele.Context, "context", "", "context to use (default: the current context)")
	tp.Flags().StringVar(&tele.Namespace, "namespace", "",
		"namespace to use (default: the current namespace for the context")
	tp.Flags().StringVar(&tele.DNSIP, "dns", "", "dns ip address")
	tp.Flags().StringVar(&tele.FallbackIP, "fallback", "", "dns fallback")
	tp.Flags().BoolVar(&tele.NoSearch, "no-search-override", false, "disable dns search override")
	tp.Flags().BoolVar(&tele.NoCheck, "no-check", false, "disable self check")

	tp.RunE = func(cmd *cobra.Command, _ []string) error {
		return teleproxy.RunTeleproxy(tele, Version)
	}

	err := tp.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
