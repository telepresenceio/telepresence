// +build windows

package teleproxy

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func Command() *cobra.Command {
	return &cobra.Command{
		Use:    "teleproxy",
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("not available on windows")
		},
	}
}
