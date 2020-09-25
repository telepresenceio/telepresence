package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/internal/pkg/edgectl/client"
	"github.com/datawire/ambassador/internal/pkg/edgectl/daemon"
	install "github.com/datawire/ambassador/internal/pkg/edgectl/install"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

func main() {
	edgectl.SetVersion(Version)

	rootCmd := getRootCommand()

	var cg []client.CmdGroup
	if client.DaemonWorks() {
		cg = []client.CmdGroup{
			{
				GroupName: "Management Commands",
				CmdNames:  []string{"install", "upgrade", "login", "license"},
			},
			{
				GroupName: "Development Commands",
				CmdNames:  []string{"status", "connect", "disconnect", "intercept"},
			},
			{
				GroupName: "Advanced Commands",
				CmdNames:  []string{"daemon", "pause", "resume", "quit"},
			},
			{
				GroupName: "Other Commands",
				CmdNames:  []string{"version", "help"},
			},
		}
	} else {
		cg = []client.CmdGroup{
			{
				GroupName: "Management Commands",
				CmdNames:  []string{"install", "upgrade", "login", "license"},
			},
			{
				GroupName: "Other Commands",
				CmdNames:  []string{"version", "help"},
			},
		}
	}

	usageFunc := client.NewCmdUsage(rootCmd, cg)
	rootCmd.SetUsageFunc(usageFunc)
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func getRootCommand() *cobra.Command {
	myName := "Edge Control"
	if !client.IsServerRunning() {
		myName = "Edge Control (daemon unavailable)"
	}

	myHelp := myName + `
  https://www.getambassador.io/docs/latest/topics/install/
`

	rootCmd := &cobra.Command{
		Use:          "edgectl",
		Short:        myName,
		Long:         myHelp,
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
	}
	_ = rootCmd.PersistentFlags().Bool(
		"no-report", false, "turn off anonymous crash reports and log submission on failure",
	)

	// Hidden/internal commands. These are called by Edge Control itself from
	// the correct context and execute in-place immediately.

	rootCmd.AddCommand(&cobra.Command{
		Use:    "daemon-foreground",
		Short:  "Launch Edge Control Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return daemon.RunAsDaemon(args[0], args[1])
		},
	})
	teleproxyCmd := &cobra.Command{
		Use:    "teleproxy",
		Short:  "Impersonate Teleproxy (for internal use)",
		Hidden: true,
	}
	teleproxyCmd.AddCommand(&cobra.Command{
		Use:    "intercept",
		Short:  "Impersonate Teleproxy Intercept (for internal use)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return daemon.RunAsTeleproxyIntercept(args[0], args[1])
		},
	})
	teleproxyCmd.AddCommand(&cobra.Command{
		Use:    "bridge",
		Short:  "Impersonate Teleproxy Bridge (for internal use)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return daemon.RunAsTeleproxyBridge(args[0], args[1])
		},
	})
	rootCmd.AddCommand(teleproxyCmd)

	// Client commands. These are never sent to the daemon.

	if client.DaemonWorks() {
		daemonCmd := &cobra.Command{
			Use:   "daemon",
			Short: "Launch Edge Control Daemon in the background (sudo)",
			Long:  daemon.Help,
			Args:  cobra.ExactArgs(0),
			RunE:  client.LaunchDaemon,
		}
		_ = daemonCmd.Flags().String(
			"dns", "",
			"DNS IP address to intercept. Defaults to the first nameserver listed in /etc/resolv.conf.",
		)
		_ = daemonCmd.Flags().String(
			"fallback", "",
			"DNS fallback, how non-cluster DNS queries are resolved. Defaults to Google DNS (8.8.8.8).",
		)
		rootCmd.AddCommand(daemonCmd)

		rootCmd.AddCommand(&cobra.Command{
			Use:   "status",
			Short: "Show connectivity status",
			Args:  cobra.ExactArgs(0),
			RunE:  client.Status,
		})

		cr := &client.ConnectInfo{}
		connectCmd := &cobra.Command{
			Use:   "connect [flags] [-- additional kubectl arguments...]",
			Short: "Connect to a cluster",
			RunE:  cr.Connect,
		}
		connectFlags := connectCmd.Flags()
		connectFlags.StringVarP(&cr.Context,
			"context", "c", "",
			"The Kubernetes context to use. Defaults to the current kubectl context.",
		)
		connectFlags.StringVarP(&cr.Namespace,
			"namespace", "n", "",
			"The Kubernetes namespace to use. Defaults to kubectl's default for the context.",
		)
		connectFlags.StringVarP(&cr.ManagerNS,
			"manager-namespace", "m", "ambassador",
			"The Kubernetes namespace in which the Traffic Manager is running.",
		)
		connectFlags.BoolVar(&cr.IsCI, "ci", false, "This session is a CI run.")
		rootCmd.AddCommand(connectCmd)

		rootCmd.AddCommand(&cobra.Command{
			Use:   "disconnect",
			Short: "Disconnect from the connected cluster",
			Args:  cobra.ExactArgs(0),
			RunE:  client.Disconnect,
		})
		rootCmd.AddCommand(&cobra.Command{
			Use:   "pause",
			Short: "Turn off network overrides (to use a VPN)",
			Args:  cobra.ExactArgs(0),
			RunE:  client.Pause,
		})
		rootCmd.AddCommand(&cobra.Command{
			Use:     "resume",
			Short:   "Turn network overrides on (after using edgectl pause)",
			Aliases: []string{"unpause"},
			Args:    cobra.ExactArgs(0),
			RunE:    client.Resume,
		})
		rootCmd.AddCommand(&cobra.Command{
			Use:   "quit",
			Short: "Tell Edge Control Daemon to quit (for upgrades)",
			Args:  cobra.ExactArgs(0),
			RunE:  client.Quit,
		})
		rootCmd.AddCommand(&cobra.Command{
			Use:   "version",
			Short: "Show program's version number and exit",
			Args:  cobra.ExactArgs(0),
			RunE:  client.Version,
		})
		interceptCmd := &cobra.Command{
			Use: "intercept",
			Long: "Manage deployment intercepts. An intercept arranges for a subset of requests to be " +
				"diverted to the local machine.",
			Short: "Manage deployment intercepts",
		}
		interceptCmd.AddCommand(&cobra.Command{
			Use:     "available",
			Aliases: []string{"avail"},
			Short:   "List deployments available for intercept",
			Args:    cobra.ExactArgs(0),
			RunE:    client.AvailableIntercepts,
		})
		interceptCmd.AddCommand(&cobra.Command{
			Use:   "list",
			Short: "List current intercepts",
			Args:  cobra.ExactArgs(0),
			RunE:  client.ListIntercepts,
		})
		interceptCmd.AddCommand(&cobra.Command{
			Use:     "remove [flags] DEPLOYMENT",
			Aliases: []string{"delete"},
			Short:   "Deactivate and remove an existent intercept",
			Args:    cobra.MinimumNArgs(1),
			RunE:    client.RemoveIntercept,
		})
		intercept := client.InterceptInfo{}
		interceptAddCmd := &cobra.Command{
			Use:   "add [flags] DEPLOYMENT -t [HOST:]PORT ([-p] | -m HEADER=REGEX ...)",
			Short: "Add a deployment intercept",
			Args:  cobra.ExactArgs(1),
			RunE:  intercept.AddIntercept,
		}
		interceptAddCmd.Flags().StringVarP(&intercept.Name, "name", "n", "", "a name for this intercept")
		interceptAddCmd.Flags().StringVar(&intercept.Prefix, "prefix", "/", "prefix to intercept")
		interceptAddCmd.Flags().BoolVarP(&intercept.Preview, "preview", "p", true, "use a preview URL") // this default is unused
		interceptAddCmd.Flags().BoolVarP(&intercept.GRPC, "grpc", "", false, "intercept GRPC traffic")
		interceptAddCmd.Flags().StringVarP(&intercept.TargetHost, "target", "t", "", "the [HOST:]PORT to forward to")
		_ = interceptAddCmd.MarkFlagRequired("target")
		interceptAddCmd.Flags().StringToStringVarP(&intercept.Patterns, "match", "m", nil, "match expression (HEADER=REGEX)")
		interceptAddCmd.Flags().StringVarP(&intercept.Namespace, "namespace", "", "", "Kubernetes namespace in which to create mapping for intercept")

		interceptCmd.AddCommand(interceptAddCmd)
		interceptCG := []client.CmdGroup{
			{
				GroupName: "Available Commands",
				CmdNames:  []string{"available", "list", "add", "remove"},
			},
		}
		interceptCmd.SetUsageFunc(client.NewCmdUsage(interceptCmd, interceptCG))
		rootCmd.AddCommand(interceptCmd)
	} else {
		rootCmd.AddCommand(&cobra.Command{
			Use:   "version",
			Short: "Show program's version number and exit",
			Args:  cobra.ExactArgs(0),
			RunE: func(_ *cobra.Command, _ []string) error {
				fmt.Println("Client", edgectl.DisplayVersion())
				fmt.Println("Daemon unavailable on this platform")
				return nil
			},
		})
	}

	loginCmd := &cobra.Command{
		Use:   "login [flags] HOSTNAME",
		Short: "Log in to the Ambassador Edge Policy Console",
		Args:  cobra.ExactArgs(1),
		RunE:  client.AESLogin,
	}
	_ = loginCmd.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = loginCmd.Flags().StringP(
		"namespace", "n", "ambassador",
		"The Kubernetes namespace to use. Defaults to ambassador.",
	)
	_ = loginCmd.Flags().Bool("url", false, "Just show the URL (don't launch a browser)")
	_ = loginCmd.Flags().Bool("token", false, "Also display the login token")
	rootCmd.AddCommand(loginCmd)
	licenseCmd := &cobra.Command{
		Use:   "license [flags] LICENSE_KEY",
		Short: "Set or update the Ambassador Edge Stack license key",
		Args:  cobra.ExactArgs(1),
		RunE:  client.AESLicense,
	}
	_ = licenseCmd.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = licenseCmd.Flags().StringP(
		"namespace", "n", "ambassador",
		"The Kubernetes namespace to use. Defaults to ambassador.",
	)
	rootCmd.AddCommand(licenseCmd)

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install the Ambassador Edge Stack in your cluster",
		Args:  cobra.ExactArgs(0),
		RunE:  install.AESInstall,
	}
	_ = installCmd.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = installCmd.Flags().BoolP(
		"verbose", "v", false,
		"Show all output. Defaults to sending most output to the logfile.",
	)
	rootCmd.AddCommand(installCmd)

	upgradeCmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade an Ambassador API Gateway installation managed by the Operator to Ambassador Edge Stack",
		Args:  cobra.ExactArgs(0),
		RunE:  install.AOSSUpgrade,
	}
	_ = upgradeCmd.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = upgradeCmd.Flags().BoolP(
		"verbose", "v", false,
		"Show all output. Defaults to sending most output to the logfile.",
	)
	rootCmd.AddCommand(upgradeCmd)

	rootCmd.InitDefaultHelpCmd()
	return rootCmd
}
