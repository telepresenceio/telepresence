package cmd

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
)

type localClientRedirectInfo struct {
	Remote string `json:"remote"`
	Local  string `json:"local"`
}

func localClientRedirectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local-client-redirect",
		Args:  OnlySubcommands,
		Short: "Manage local client redirects",
		RunE:  RunSubcommands,
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{"add", "list", "remove"}, cobra.ShellCompDirectiveNoFileComp
		},
	}
	cmd.AddCommand(localClientRedirectAddCmd(), localClientRedirectListCmd(), localClientRedirectRemoveCmd())
	return cmd
}

func localClientRedirectAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <host>:<port>:<local-port>[/{tcp,udp}]",
		Args:  cobra.ExactArgs(1),
		Short: "Redirect client traffic for a remote host and port to localhost",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := connect.InitCommand(cmd); err != nil {
				return err
			}
			defer progress.Stop(cmd.Context())
			ctx := cmd.Context()
			if err := connect.ResolveLocalClientRedirect(ctx, daemon.MustGetSession(ctx), args[0]); err != nil {
				return err
			}
			if output.WantsFormatted(cmd) {
				output.Object(ctx, map[string]string{"redirect": args[0]}, false)
				return nil
			}
			_, err := fmt.Fprintf(output.Out(ctx), "Added local client redirect %s\n", args[0])
			return err
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
}

func localClientRedirectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		Short:   "List local client redirects",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := connect.InitCommand(cmd); err != nil {
				return err
			}
			defer progress.Stop(cmd.Context())
			ctx := cmd.Context()
			redirects, err := getLocalClientRedirectInfos(ctx, daemon.MustGetSession(ctx))
			if err != nil {
				return err
			}
			if output.WantsFormatted(cmd) {
				output.Object(ctx, redirects, false)
				return nil
			}
			if len(redirects) == 0 {
				_, err = fmt.Fprintln(output.Out(ctx), "No local client redirects")
				return err
			}
			for _, redirect := range redirects {
				if _, err = fmt.Fprintf(output.Out(ctx), "%s -> %s\n", redirect.Remote, redirect.Local); err != nil {
					return err
				}
			}
			return nil
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
}

func localClientRedirectRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <host>:<port>[/{tcp,udp}]",
		Aliases: []string{"rm", "delete"},
		Args:    cobra.ExactArgs(1),
		Short:   "Remove a local client redirect",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := connect.InitCommand(cmd); err != nil {
				return err
			}
			defer progress.Stop(cmd.Context())
			ctx := cmd.Context()
			if err := connect.RemoveLocalClientRedirect(ctx, daemon.MustGetSession(ctx), args[0]); err != nil {
				return err
			}
			if output.WantsFormatted(cmd) {
				output.Object(ctx, map[string]string{"remote": args[0]}, false)
				return nil
			}
			_, err := fmt.Fprintf(output.Out(ctx), "Removed local client redirect %s\n", args[0])
			return err
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
}

func getLocalClientRedirectInfos(ctx context.Context, session *daemon.Session) ([]localClientRedirectInfo, error) {
	redirects, err := connect.ListLocalClientRedirects(ctx, session)
	if err != nil {
		return nil, err
	}
	infos := make([]localClientRedirectInfo, 0, len(redirects))
	for _, redirect := range redirects {
		infos = append(infos, localClientRedirectInfo{
			Remote: redirect.Remote.String(),
			Local:  fmt.Sprintf("127.0.0.1:%d", redirect.LocalPort),
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Remote == infos[j].Remote {
			return infos[i].Local < infos[j].Local
		}
		return infos[i].Remote < infos[j].Remote
	})
	return infos, nil
}
