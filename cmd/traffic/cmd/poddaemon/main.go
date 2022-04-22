package poddaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/spf13/cobra"

	rpc_userd "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc_manager "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
)

const processName = "pod-daemon"

type Args struct {
	WorkloadKind      string
	WorkloadName      string
	WorkloadNamespace string

	Port int32

	IngressHost   string
	IngressPort   int32
	IngressTLS    bool
	IngressL5Host string

	PullRequestURL string

	AddRequestHeaders map[string]string

	CloudAPIKey string
}

// PodNamespace is borrowed from
// "k8s.io/client-go/tools/clientcmd".inClusterConfig.Namespace()
func PodNamespace() string {
	// This way assumes you've set the POD_NAMESPACE environment variable using the downward API.
	// This check has to be done first for backwards compatibility with the way InClusterConfig was originally set up
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}

	// Fall back to the namespace associated with the service account token, if available
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}

	return "default"
}

func Main(ctx context.Context, argStrs ...string) error {
	ctx = dgroup.WithGoroutineName(ctx, "/"+processName)

	var args Args
	cmd := &cobra.Command{
		Use:  "pod-daemon",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Passing secrets as CLI arguments is an anti-pattern, so take it from an
			// env-var.
			args.CloudAPIKey = os.Getenv("AMBASSADOR_CLOUD_APIKEY")

			if args.IngressL5Host == "" {
				args.IngressL5Host = args.IngressHost
			}

			return main(cmd.Context(), args)
		},
		SilenceErrors: true, // main() will handle it after we return from .ExecuteContext()
		SilenceUsage:  true, // our FlagErrorFunc will handle it
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		// Taken from telepresence history: see commit fa528109ce246e77bb187f4f46a110562fad3d72
		if err == nil {
			return nil
		}

		// If the error is multiple lines, include an extra blank line before the "See
		// --help" line.
		errStr := strings.TrimRight(err.Error(), "\n")
		if strings.Contains(errStr, "\n") {
			errStr += "\n"
		}

		fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\nSee '%s --help'.\n", cmd.CommandPath(), errStr, cmd.CommandPath())
		os.Exit(2)
		return nil
	})

	cmd.Flags().StringVar(&args.WorkloadKind, "workload-kind", "Deployment",
		"TODO")
	cmd.Flags().StringVar(&args.WorkloadName, "workload-name", "",
		"TODO")
	cmd.Flags().StringVar(&args.WorkloadNamespace, "workload-namespace", PodNamespace(),
		"TODO")
	cmd.Flags().Int32Var(&args.Port, "port", 8080,
		"TODO")

	cmd.Flags().StringVar(&args.IngressHost, "ingress-host", "",
		"L3 hostname (IP address or DNS name) of the relevant ingress")
	cmd.Flags().Int32Var(&args.IngressPort, "ingress-port", 443,
		"L4 TCP port number of the relevant ingress")
	cmd.Flags().BoolVar(&args.IngressTLS, "ingress-tls", true,
		"Whether the relevant ingress uses TLS on that port")
	cmd.Flags().StringVar(&args.IngressL5Host, "ingress-l5host", "",
		"Hostname to put in requests (TLS-SNI and the HTTP \"Host\" header) (default is to use the L3 hostname)")

	cmd.Flags().StringVar(&args.PullRequestURL, "pull-request", "",
		"TODO")
	cmd.Flags().StringToStringVarP(&args.AddRequestHeaders, "preview-url-add-request-headers", "", map[string]string{},
		"Additional headers in key1=value1,key2=value2 pairs injected in every preview page request")

	cmd.SetArgs(argStrs)
	return cmd.ExecuteContext(ctx)
}

// main in mostly mimics pkg/client/userd.run(), but is trimmed down for running in a Pod.
func main(ctx context.Context, args Args) error {
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	ctx = client.WithConfig(ctx, cfg)
	env, err := client.LoadEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to load env: %w", err)
	}
	ctx = client.WithEnv(ctx, env)

	loginExecutor := loginExecutor{key: args.CloudAPIKey}

	scoutReporter := scout.NewReporter(ctx, processName)
	userdCoreImpl := userd.GetPoddService(scoutReporter, *cfg, loginExecutor)

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	grp.Go("telemetry", scoutReporter.Run)
	grp.Go("session", func(ctx context.Context) error {
		return userdCoreImpl.ManageSessions(ctx, []trafficmgr.SessionService{})
	})
	podd := true
	grp.Go("main", func(ctx context.Context) error {
		dlog.Infof(ctx, "Connecting to traffic manager...")
		cResp, err := userdCoreImpl.Connect(ctx, &rpc_userd.ConnectRequest{
			// I don't think we need to set anything here.
			KubeFlags:        nil, // nil should be fine since we're in-cluster
			MappedNamespaces: nil, // we're not doing networking things.
			Podd:             &podd,
		})
		if err != nil {
			return err
		}
		if err := connectError(cResp); err != nil {
			return err
		}
		session := cResp.SessionInfo
		dlog.Infof(ctx, "Connected to traffic manager: session=%v", session)

		dlog.Infof(ctx, "Creating intercept...")
		iResp, err := userdCoreImpl.CreatePoddIntercept(ctx, &rpc_userd.CreateInterceptRequest{
			Spec: &rpc_manager.InterceptSpec{
				Name:         args.WorkloadName,
				Client:       "", // empty for CreateInterceptRequest
				Agent:        args.WorkloadName,
				WorkloadKind: args.WorkloadKind,
				Namespace:    args.WorkloadNamespace,
				Mechanism:    "http",
				TargetHost:   "127.0.0.1",
				TargetPort:   args.Port,
				ServiceName:  args.WorkloadName,
			},
			MountPoint: "", // we're not mounting things
			AgentImage: "docker.io/datawire/ambassador-telepresence-agent:1.11.10",
		})
		if err != nil {
			return err
		}
		if err := cli.InterceptError(iResp); err != nil {
			return err
		}
		dlog.Infof(ctx, "Created intercept")

		dlog.Infof(ctx, "Creating preview URL...")
		uResp, err := userdCoreImpl.ManagerProxy.UpdateIntercept(ctx, &rpc_manager.UpdateInterceptRequest{
			Session: session,
			Name:    args.WorkloadName,
			PreviewDomainAction: &rpc_manager.UpdateInterceptRequest_AddPreviewDomain{
				AddPreviewDomain: &rpc_manager.PreviewSpec{
					PullRequestUrl:    args.PullRequestURL,
					AddRequestHeaders: args.AddRequestHeaders,
					DisplayBanner:     true,
					Ingress: &rpc_manager.IngressInfo{
						Host:   args.IngressHost,
						Port:   args.IngressPort,
						UseTls: args.IngressTLS,
						L5Host: args.IngressL5Host,
					},
				},
			},
		})
		if err != nil {
			return err
		}
		dlog.Infof(ctx, "Created preview URL: %q", "https://"+uResp.PreviewDomain)

		// Watch the intercept so that we can report errors.
		var prevSummary string
		return userdCoreImpl.ManagerProxy.WatchIntercepts(session, &interceptWatcher{
			ctx: ctx,
			handler: func(snapshot *rpc_manager.InterceptInfoSnapshot) error {
				switch len(snapshot.Intercepts) {
				case 0:
					return fmt.Errorf("intercept vanished; restarting")
				case 1:
					nextSummary, ok := summarizeIntercept(snapshot.Intercepts[0])
					if nextSummary != prevSummary {
						dlog.Infoln(ctx, nextSummary)
					}
					if !ok {
						return errors.New(nextSummary)
					}
					prevSummary = nextSummary
					return nil
				default:
					return fmt.Errorf("this %v has multiple intercepts associated with it... that doesn't make sense", processName)
				}
			},
		})
	})

	return grp.Wait()
}

func connectError(info *rpc_userd.ConnectInfo) error {
	switch info.Error {
	case rpc_userd.ConnectInfo_UNSPECIFIED, rpc_userd.ConnectInfo_ALREADY_CONNECTED:
		return nil
	case rpc_userd.ConnectInfo_MUST_RESTART:
		return fmt.Errorf("connected, but kubeconfig has changed")
	case rpc_userd.ConnectInfo_CLUSTER_FAILED:
		return fmt.Errorf("error talking to cluster: %s", info.ErrorText)
	case rpc_userd.ConnectInfo_TRAFFIC_MANAGER_FAILED:
		return fmt.Errorf("error talking to traffic manager: %s", info.ErrorText)
	default: // DISCONNECTED, DAEMON_FAILED, or unknown
		return fmt.Errorf("unexpected error code: code=%v text=%q category=%v",
			info.Error, info.ErrorText, info.ErrorCategory)
	}
}

func summarizeIntercept(icept *rpc_manager.InterceptInfo) (summary string, iceptIsOK bool) {
	iceptIsOK = true
	summary = fmt.Sprintf("intercept name=%q (id=%q) state: ", icept.Spec.Name, icept.Id)
	if icept.Disposition > rpc_manager.InterceptDispositionType_WAITING {
		summary += "error: "
		iceptIsOK = false
	}
	summary += icept.Disposition.String()
	if icept.Message != "" {
		summary += ": " + icept.Message
	}
	return
}

type interceptWatcher struct {
	ctx     context.Context
	handler func(*rpc_manager.InterceptInfoSnapshot) error
	shamServerStream
}

func (iw *interceptWatcher) Context() context.Context {
	return iw.ctx
}

func (iw *interceptWatcher) Send(arg *rpc_manager.InterceptInfoSnapshot) error {
	return iw.handler(arg)
}
