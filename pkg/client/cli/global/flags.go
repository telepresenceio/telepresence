package global

import (
	"github.com/spf13/pflag"
)

const (
	FlagDocker   = "docker"
	FlagContext  = "context"
	FlagOutput   = "output"
	FlagNoReport = "no-report"
)

func Flags(hasKubeFlags bool) *pflag.FlagSet {
	flags := pflag.NewFlagSet("", 0)
	if !hasKubeFlags {
		flags.String(FlagContext, "", "The name of the kubeconfig context to use")
	}
	flags.Bool(FlagDocker, false, "Start, or connect to, daemon in a docker container")
	flags.Bool(FlagNoReport, false, "Turn off anonymous crash reports and log submission on failure")
	flags.String(FlagOutput, "default", "Set the output format, supported values are 'json', 'yaml', and 'default'")
	return flags
}
