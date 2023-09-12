package global

import (
	"github.com/spf13/pflag"
)

const (
	FlagDocker   = "docker"
	FlagContext  = "context"
	FlagUse      = "use"
	FlagOutput   = "output"
	FlagNoReport = "no-report"
)

func Flags(hasKubeFlags bool) *pflag.FlagSet {
	flags := pflag.NewFlagSet("", 0)
	if !hasKubeFlags {
		// Add deprecated global connect and docker flags.
		flags.String(FlagContext, "", "The name of the kubeconfig context to use")
		flags.Lookup(FlagContext).Hidden = true
		flags.Bool(FlagDocker, false, "Start, or connect to, daemon in a docker container")
		flags.Lookup(FlagDocker).Hidden = true
	}
	flags.Bool(FlagNoReport, false, "Turn off anonymous crash reports and log submission on failure")
	flags.String(FlagUse, "", "Match expression that uniquely identifies the daemon container")
	flags.String(FlagOutput, "default", "Set the output format, supported values are 'json', 'yaml', and 'default'")
	return flags
}
