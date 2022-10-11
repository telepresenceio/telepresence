package util

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type subCommandsKey struct{}

func AddSubCommands(ctx context.Context, commands []*cobra.Command) context.Context {
	if ecs, ok := ctx.Value(subCommandsKey{}).(*[]*cobra.Command); ok {
		*ecs = mergeCommands(*ecs, commands)
	} else {
		ctx = context.WithValue(ctx, subCommandsKey{}, &commands)
	}
	return ctx
}

func GetSubCommands(cmd *cobra.Command) []*cobra.Command {
	if gs, ok := cmd.Context().Value(subCommandsKey{}).(*[]*cobra.Command); ok {
		return *gs
	}
	return nil
}

// mergeCommands merges the command slice b into a, replacing commands using the same name
// and returns the resulting slice.
func mergeCommands(a, b []*cobra.Command) []*cobra.Command {
	ac := make(map[string]*cobra.Command, len(a)+len(b))
	for _, c := range a {
		ac[c.Name()] = c
	}
	for _, c := range b {
		ac[c.Name()] = c
	}
	return maps.ToSortedSlice(ac)
}
