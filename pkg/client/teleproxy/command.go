// +build !windows

package teleproxy

import (
	"os"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
)

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "teleproxy",
		Short:  "Impersonate Teleproxy (for internal use)",
		Hidden: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use:    "intercept",
		Short:  "Impersonate Teleproxy Intercept (for internal use)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runAsIntercept(args[0], args[1])
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:    "bridge",
		Short:  "Impersonate Teleproxy Bridge (for internal use)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runAsBridge(args[0], args[1])
		},
	})
	return cmd
}

// runAsIntercept is the main function when executing as teleproxy intercept
func runAsIntercept(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("telepresence daemon as teleproxy intercept must run as root")
	}
	t := &config{
		Mode:       interceptMode,
		DNSIP:      dns,
		FallbackIP: fallback,
	}
	return t.run(client.DisplayVersion())
}

// runAsBridge is the main function when executing as teleproxy bridge
func runAsBridge(context, namespace string) error {
	t := &config{
		Mode:      bridgeMode,
		Context:   context,
		Namespace: namespace,
	}
	return t.run(client.DisplayVersion())
}
