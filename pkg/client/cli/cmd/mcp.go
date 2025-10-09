package cmd

import (
	"github.com/njayp/ophis"
	"github.com/spf13/cobra"
)

func mcp() *cobra.Command {
	return ophis.Command(&ophis.Config{
		Selectors: []ophis.Selector{
			{
				CmdSelector: ophis.AllowCmds("telepresence connect"),
				// allow all local flags except kubeflags
				LocalFlagSelector: ophis.ExcludeFlags(
					"as",
					"as-group",
					"as-uid",
					"cache-dir",
					"certificate-authority",
					"client-certificate",
					"client-key",
					"cluster",
					"context",
					"disable-compression",
					"insecure-skip-tls-verify",
					"kubeconfig",
					"request-timeout",
					"server",
					"tls-server-name",
					"token",
					"user",
				),
				InheritedFlagSelector: ophis.NoFlags,
			},
			{
				CmdSelector: ophis.AllowCmds(
					"telepresence quit",
					"telepresence status",
				),

				// no local or global flags
				LocalFlagSelector:     ophis.NoFlags,
				InheritedFlagSelector: ophis.NoFlags,
			},
			{
				CmdSelector: ophis.AllowCmds(
					"telepresence intercept",
					"telepresence ingest",
					"telepresence leave",
					"telepresence list",
					"telepresence wiretap",
					"telepresence replace",
				),

				// allow local flags
				// allow global output flag for `--detailed-output` and `--output json` combo
				InheritedFlagSelector: ophis.AllowFlags("output"),
			},
		},
	})
}
