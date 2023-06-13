package cmd

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func testVPN() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test-vpn",
		Args:  cobra.NoArgs,
		Short: "Test VPN configuration for compatibility with telepresence",
		RunE: func(_ *cobra.Command, _ []string) error {
			return errcat.User.New("the test-vpn command is deprecated." +
				" Please see https://www.getambassador.io/docs/telepresence/latest/reference/vpn" +
				" to learn how to configure telepresence for your VPN.")
		},
	}
	return cmd
}
