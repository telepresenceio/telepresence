package edgectl

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/ambassador/pkg/supervisor"
)

func (d *Daemon) handleCommand(p *supervisor.Process, conn net.Conn, data *ClientMessage) error {
	out := NewEmitter(conn)
	rootCmd := d.GetRootCommand(p, out, data)
	rootCmd.SetOutput(conn) // FIXME replace with SetOut and SetErr
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		if batch, _ := cmd.Flags().GetBool("batch"); batch {
			out.SetKV()
		}
	}
	rootCmd.SetArgs(data.Args[1:])
	err := rootCmd.Execute()
	if err != nil {
		out.SendExit(1)
	}
	return out.Err()
}

func (d *Daemon) GetRootCommand(p *supervisor.Process, out *Emitter, data *ClientMessage) *cobra.Command {
	reporter := &metriton.Reporter{
		Application:  "edgectl",
		Version:      Version,
		GetInstallID: func(_ *metriton.Reporter) (string, error) { return data.InstallID, nil },
		BaseMetadata: map[string]interface{}{"mode": "daemon"},
	}

	rootCmd := &cobra.Command{
		Use:          "edgectl",
		Short:        "Edge Control",
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
	}
	_ = rootCmd.PersistentFlags().Bool("batch", false, "Emit machine-readable output")
	_ = rootCmd.PersistentFlags().MarkHidden("batch")

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Show program's version number and exit",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			out.Println("Client", data.ClientVersion)
			out.Println("Daemon", DisplayVersion())
			out.Send("daemon.version", Version)
			out.Send("daemon.apiVersion", apiVersion)
			return out.Err()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show connectivity status",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := d.Status(p, out); err != nil {
				return err
			}
			return out.Err()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "pause",
		Short: "Turn off network overrides (to use a VPN)",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			if d.network == nil {
				out.Println("Network overrides are already paused")
				out.Send("paused", true)
				return out.Err()
			}
			if d.cluster != nil {
				out.Println("Edge Control is connected to a cluster.")
				out.Println("See \"edgectl status\" for details.")
				out.Println("Please disconnect before pausing.")
				out.Send("paused", false)
				out.SendExit(1)
				return out.Err()
			}

			if err := d.network.Close(); err != nil {
				p.Logf("pause: %v", err)
				out.Printf("Unexpected error while pausing: %v\n", err)
			}
			d.network = nil

			out.Println("Network overrides paused.")
			out.Println("Use \"edgectl resume\" to reestablish network overrides.")
			out.Send("paused", true)

			return out.Err()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:     "resume",
		Short:   "Turn network overrides on (after using edgectl pause)",
		Aliases: []string{"unpause"},
		Args:    cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			if d.network != nil {
				if d.network.IsOkay() {
					out.Println("Network overrides are established (not paused)")
				} else {
					out.Println("Network overrides are being reestablished...")
				}
				out.Send("paused", false)
				return out.Err()
			}

			if err := d.MakeNetOverride(p); err != nil {
				p.Logf("resume: %v", err)
				out.Printf("Unexpected error establishing network overrides: %v", err)
			}
			out.Send("paused", d.network == nil)

			return out.Err()
		},
	})
	connectCmd := &cobra.Command{
		Use:   "connect [flags] [-- additional kubectl arguments...]",
		Short: "Connect to a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := reporter.Report(p.Context(), map[string]interface{}{"action": "connect"}); err != nil {
				p.Logf("report failed: %+v", err)
			}
			context, _ := cmd.Flags().GetString("context")
			namespace, _ := cmd.Flags().GetString("namespace")
			managerNs, _ := cmd.Flags().GetString("manager-namespace")
			isCI, _ := cmd.Flags().GetBool("ci")
			if err := d.Connect(
				p, out, data.RAI,
				context, namespace, managerNs, args,
				data.InstallID, isCI,
			); err != nil {
				return err
			}
			return out.Err()
		},
	}
	_ = connectCmd.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = connectCmd.Flags().StringP(
		"namespace", "n", "",
		"The Kubernetes namespace to use. Defaults to kubectl's default for the context.",
	)
	_ = connectCmd.Flags().StringP(
		"manager-namespace", "m", "ambassador",
		"The Kubernetes namespace in which the Traffic Manager is running.",
	)
	_ = connectCmd.Flags().Bool("ci", false, "This session is a CI run.")
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(&cobra.Command{
		Use:   "disconnect",
		Short: "Disconnect from the connected cluster",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := d.Disconnect(p, out); err != nil {
				return err
			}
			return out.Err()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "quit",
		Short: "Tell Edge Control Daemon to quit (for upgrades)",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			out.Println("Edge Control Daemon quitting...")
			out.Send("quit", true)
			p.Supervisor().Shutdown()
			return out.Err()
		},
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
		RunE: func(_ *cobra.Command, _ []string) error {
			msg := d.interceptMessage()
			if msg != "" {
				out.Println(msg)
				out.Send("intercept", msg)
				return out.Err()
			}
			out.Send("interceptable", len(d.trafficMgr.interceptables))
			switch {
			case len(d.trafficMgr.interceptables) == 0:
				out.Println("No interceptable deployments")
			default:
				out.Printf("Found %d interceptable deployment(s):\n", len(d.trafficMgr.interceptables))
				for idx, deployment := range d.trafficMgr.interceptables {
					fields := strings.SplitN(deployment, "/", 2)

					appName := fields[0]
					appNamespace := d.cluster.namespace

					if len(fields) > 1 {
						appNamespace = fields[0]
						appName = fields[1]
					}

					out.Printf("%4d. %s in namespace %s\n", idx+1, appName, appNamespace)
					out.Send(fmt.Sprintf("interceptable.deployment.%d", idx+1), deployment)
				}
			}
			return out.Err()
		},
	})

	interceptCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List current intercepts",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := d.ListIntercepts(p, out); err != nil {
				return err
			}
			return out.Err()
		},
	})
	interceptCmd.AddCommand(&cobra.Command{
		Use:     "remove [flags] DEPLOYMENT",
		Aliases: []string{"delete"},
		Short:   "Deactivate and remove an existent intercept",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if err := d.RemoveIntercept(p, out, name); err != nil {
				return err
			}
			return out.Err()
		},
	})
	intercept := InterceptInfo{}
	interceptPreview := true
	var interceptAddCmdFlags *pflag.FlagSet
	interceptAddCmd := &cobra.Command{
		Use:   "add [flags] DEPLOYMENT -t [HOST:]PORT ([-p] | -m HEADER=REGEX ...)",
		Short: "Add a deployment intercept",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			intercept.Deployment = args[0]
			if intercept.Name == "" {
				intercept.Name = fmt.Sprintf("cept-%d", time.Now().Unix())
			}

			// if intercept.Namespace == "" {
			// 	intercept.Namespace = "default"
			// }

			if intercept.Prefix == "" {
				intercept.Prefix = "/"
			}

			var host, portStr string
			hp := strings.SplitN(intercept.TargetHost, ":", 2)
			if len(hp) < 2 {
				portStr = hp[0]
			} else {
				host = strings.TrimSpace(hp[0])
				portStr = hp[1]
			}
			if len(host) == 0 {
				host = "127.0.0.1"
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				out.Printf("Failed to parse %q as HOST:PORT: %v\n", intercept.TargetHost, err)
				out.Send("failed", "parse target")
				out.SendExit(1)
				return nil
			}
			intercept.TargetHost = host
			intercept.TargetPort = port

			// If the user specifies --preview on the command line, then use its
			// value (--preview is the same as --preview=true, or it could be
			// --preview=false). But if the user does not specify --preview on
			// the command line, compute its value from the presence or absence
			// of --match, since they are mutually exclusive.
			userSetPreviewFlag := interceptAddCmdFlags.Changed("preview")
			userSetMatchFlag := len(intercept.Patterns) > 0

			if userSetPreviewFlag && interceptPreview {
				// User specified --preview (or --preview=true) at the command line
				if userSetMatchFlag {
					out.Println("Error: Cannot use --match and --preview at the same time")
					out.Send("failed", "both match and preview")
					out.SendExit(1)
					return nil
				} else {
					// ok: --preview=true and no --match
				}
			} else if userSetPreviewFlag && !interceptPreview {
				// User specified --preview=false at the command line
				if userSetMatchFlag {
					// ok: --preview=false and at least one --match
				} else {
					out.Println("Error: Must specify --match when using --preview=false")
					out.Send("failed", "neither match nor preview")
					out.SendExit(1)
					return nil
				}
			} else {
				// User did not specify --preview at the command line
				if userSetMatchFlag {
					// ok: at least one --match
					interceptPreview = false
				} else {
					// ok: neither --match nor --preview, fall back to preview
					interceptPreview = true
				}
			}

			if interceptPreview {
				if d.trafficMgr.previewHost == "" {
					out.Println("Your cluster is not configured for Preview URLs.")
					out.Println("(Could not find a Host resource that enables Path-type Preview URLs.)")
					out.Println("Please specify one or more header matches using --match.")
					out.Send("failed", "preview requested but unavailable")
					out.SendExit(1)
					return nil
				}

				intercept.Patterns = make(map[string]string)
				intercept.Patterns["x-service-preview"] = data.InstallID
			}

			if err := d.AddIntercept(p, out, &intercept); err != nil {
				return err
			}

			if url := intercept.PreviewURL(d.trafficMgr.previewHost); url != "" {
				out.Println("Share a preview of your changes with anyone by visiting\n  ", url)
			}

			return out.Err()
		},
	}
	interceptAddCmd.Flags().StringVarP(&intercept.Name, "name", "n", "", "a name for this intercept")
	interceptAddCmd.Flags().StringVar(&intercept.Prefix, "prefix", "/", "prefix to intercept")
	interceptAddCmd.Flags().BoolVarP(&interceptPreview, "preview", "p", true, "use a preview URL") // this default is unused
	interceptAddCmd.Flags().StringVarP(&intercept.TargetHost, "target", "t", "", "the [HOST:]PORT to forward to")
	_ = interceptAddCmd.MarkFlagRequired("target")
	interceptAddCmd.Flags().StringToStringVarP(&intercept.Patterns, "match", "m", nil, "match expression (HEADER=REGEX)")
	interceptAddCmd.Flags().StringVarP(&intercept.Namespace, "namespace", "", "", "Kubernetes namespace in which to create mapping for intercept")
	interceptAddCmdFlags = interceptAddCmd.Flags()

	interceptCmd.AddCommand(interceptAddCmd)
	interceptCG := []CmdGroup{
		{
			GroupName: "Available Commands",
			CmdNames:  []string{"available", "list", "add", "remove"},
		},
	}
	interceptCmd.SetUsageFunc(NewCmdUsage(interceptCmd, interceptCG))
	rootCmd.AddCommand(interceptCmd)

	return rootCmd
}
