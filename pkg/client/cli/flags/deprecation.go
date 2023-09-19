package flags

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// DeprecationIfChanged will print a deprecation warning on output.Info if the flag has changed.
//
// Use this method instead of the standard pflag deprecation to ensure that the deprecation message
// doesn't clobber JSON output.
func DeprecationIfChanged(cmd *cobra.Command, flagName, alternative string) {
	if flag := cmd.Flag(flagName); flag != nil && flag.Changed {
		ioutil.Printf(output.Info(cmd.Context()), "Flag --%s has been deprecated, %s\n", flagName, alternative)
	}
}
