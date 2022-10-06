package cliutil

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// FlagGroup represents a group of flags and the name of that group.
type FlagGroup struct {
	Name  string
	Flags *pflag.FlagSet
}

type CommandGroups map[string][]*cobra.Command

type commandGroupKey struct{}

func AddCommandGroups(ctx context.Context, groups CommandGroups) context.Context {
	if egs, ok := ctx.Value(commandGroupKey{}).(CommandGroups); ok {
		egs.merge(groups)
	} else {
		ctx = context.WithValue(ctx, commandGroupKey{}, groups)
	}
	return ctx
}

func GetCommandGroups(cmd *cobra.Command) CommandGroups {
	if gs, ok := cmd.Context().Value(commandGroupKey{}).(CommandGroups); ok {
		return gs
	}
	return nil
}

func (gs CommandGroups) merge(ogs CommandGroups) {
	indexOf := func(g []*cobra.Command, cmd *cobra.Command) int {
		n := cmd.Name()
		for i, c := range g {
			if c.Name() == n {
				return i
			}
		}
		return -1
	}

	for n, g := range ogs {
		if eg, ok := gs[n]; ok {
			// merge the command slice g into eg, replacing commands using the same name
			for _, c := range g {
				if i := indexOf(eg, c); i >= 0 {
					eg[i] = c
				} else {
					eg = append(eg, c)
				}
			}
			g = eg
		}
		gs[n] = g
	}
}
