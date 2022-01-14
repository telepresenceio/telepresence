package cliutil

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type CommandGroups map[string][]*cobra.Command

// FlagGroup represents a group of flags and the name of that group
type FlagGroup struct {
	Name  string
	Flags *pflag.FlagSet
}
