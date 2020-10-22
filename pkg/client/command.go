package client

import (
	"fmt"
	"strings"

	"github.com/datawire/telepresence2/pkg/connector"
	"github.com/datawire/telepresence2/pkg/daemon"
	"github.com/datawire/telepresence2/pkg/teleproxy"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var Help = `telepresence can run a command in a sub shell after ensuring that a connection
has been established with a Traffic Manager and optionally also that an intercept has
been added.

The command ensures that only those resources that were acquired are cleaned up. This
means that the telepresence daemon will not quit if it was already started, no disconnect
will take place if the connection was already established, and the intercept will not be
removed if it was already added.

Unless the daemon is already started, an attempt will be made to start it. This will
involve a call to sudo unless this command is run as root (not recommended).

run a command with an intercept in place:
    telepresence --intercept hello -port 9000 -- <command> arguments...
`

func Command() *cobra.Command {
	myName := "Telepresence"
	if !IsServerRunning() {
		myName = "Telepresence (daemon unavailable)"
	}

	myHelp := myName + `
  https://www.getambassador.io/docs/latest/topics/install/
`

	r := &runner{}
	rootCmd := &cobra.Command{
		Use:          "telepresence",
		Short:        myName,
		Long:         myHelp,
		Args:         cobra.ArbitraryArgs,
		RunE:         r.run,
		PreRunE:      checkFlags,
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
	}
	_ = rootCmd.PersistentFlags().Bool(
		"no-report", false, "turn off anonymous crash reports and log submission on failure",
	)

	// Hidden/internal commands. These are called by Telepresence itself from
	// the correct context and execute in-place immediately.
	rootCmd.AddCommand(daemon.Command())
	rootCmd.AddCommand(connector.Command())
	rootCmd.AddCommand(teleproxy.Command())

	// Client commands. These are never sent to the daemon.
	flags := rootCmd.Flags()
	flags.BoolVarP(&r.NoWait,
		"no-wait", "", false,
		"Give back the original prompt instead of running a subshell",
	)
	flags.BoolVarP(&r.Status,
		"status", "", false,
		"Show connectivity status",
	)
	flags.BoolVarP(&r.Quit,
		"quit", "", false,
		"Tell daemon to quit. Only meaningful after using --no-wait",
	)
	flags.BoolVarP(&r.Version,
		"version", "", false,
		"Show program's version number and exit",
	)
	flags.StringVarP(&r.DNS,
		"dns", "", "",
		"DNS IP address to intercept. Defaults to the first nameserver listed in /etc/resolv.conf.",
	)
	flags.StringVarP(&r.Fallback,
		"fallback", "", "",
		"DNS fallback, how non-cluster DNS queries are resolved. Defaults to Google DNS (8.8.8.8).",
	)
	flags.StringVarP(&r.Context,
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	flags.StringVarP(&r.ConnectRequest.Namespace,
		"namespace", "n", "",
		"The Kubernetes namespace to use. Defaults to kubectl's default for the context.",
	)
	flags.StringVarP(&r.ManagerNs,
		"manager-namespace", "", "ambassador",
		"The Kubernetes namespace in which the Traffic Manager is running.",
	)
	flags.BoolVar(&r.IsCi, "ci", false, "This session is a CI run.")
	flags.StringVarP(&r.Deployment, "intercept", "i", "", "Name of deployment to intercept")
	flags.StringVarP(&r.Name, "name", "", "", "Name of the intercept")
	flags.StringVar(&r.Prefix, "prefix", "/", "prefix to intercept")
	flags.BoolVarP(&r.Preview, "preview", "p", true, "use a preview URL") // this default is unused
	flags.BoolVarP(&r.Grpc, "grpc", "", false, "intercept GRPC traffic")
	flags.StringVarP(&r.TargetHost, "port", "", "", "the local port to forward to")
	flags.StringToStringVarP(&r.Patterns, "match", "m", nil, "match expression (HEADER=REGEX)")
	rootCmd.InitDefaultHelpCmd()
	return rootCmd
}

var flagRules = map[string][]string{
	"version":   nil,                       // cannot be combined with other flags
	"quit":      nil,                       // cannot be combined with other flags
	"status":    nil,                       // cannot be combined with other flags
	"intercept": {"port"},                  // intercept requires port
	"grpc":      {"intercept"},             // grpc requires intercept
	"match":     {"intercept", "!preview"}, // match requires intercept and can not be combined with preview
	"name":      {"intercept"},             // name requires intercept
	"port":      {"intercept"},             // port requires intercept
	"prefix":    {"intercept"},             // prefix requires intercept
	"preview":   {"intercept", "!match"},   // preview requires intercept and can not be combined with match
}

func checkFlags(cmd *cobra.Command, _ []string) (err error) {
	flags := cmd.Flags()
	flags.Visit(func(f *pflag.Flag) {
		if err != nil {
			return
		}
		if f.Value.Type() == "bool" && f.Value.String() == "false" {
			// consider unset
			return
		}
		rules, ok := flagRules[f.Name]
		if !ok {
			return
		}
		if rules == nil {
			if flags.NFlag() > 1 {
				err = fmt.Errorf("flag --%s cannot be combined with another flag", f.Name)
			}
			if flags.NArg() > 0 {
				err = fmt.Errorf("flag --%s does not expect any arguments", f.Name)
			}
			return
		}

		for _, rule := range rules {
			me := strings.HasPrefix(rule, "!")
			if me {
				rule = rule[1:]
			}
			rf := flags.Lookup(rule)
			if rf.Changed {
				if me {
					if !(rf.Value.Type() == "bool" && rf.Value.String() == "false") {
						err = fmt.Errorf("flag --%s can not be used in combination with flag --%s", f.Name, rf.Name)
					}
				}
			} else if !me {
				err = fmt.Errorf("flag --%s must be used in combination with flag --%s", f.Name, rule)
			}
		}
	})
	return err
}
