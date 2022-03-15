package main

import (
	"context"
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
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

const processName = "pod-daemon"

type Args struct {
	WorkloadKind      string
	WorkloadName      string
	WorkloadNamespace string

	Port int32

	PullRequestURL string

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

func main() {
	ctx := context.Background()
	ctx = log.MakeBaseLogger(ctx, os.Getenv("LOG_LEVEL"))
	ctx = dgroup.WithGoroutineName(ctx, "/"+processName)

	var args Args
	cmd := &cobra.Command{
		Use:  "podd",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Passing secrets as CLI arguments is an anti-pattern, so take it from an
			// env-var.
			args.CloudAPIKey = os.Getenv("AMBASSADOR_CLOUD_APIKEY")

			return Main(cmd.Context(), args)
		},
	}
	cmd.Flags().StringVar(&args.WorkloadKind, "workload-kind", "deployment",
		"TODO")
	cmd.Flags().StringVar(&args.WorkloadName, "workload-name", "",
		"TODO")
	cmd.Flags().StringVar(&args.WorkloadNamespace, "workload-namespace", PodNamespace(),
		"TODO")
	cmd.Flags().Int32Var(&args.Port, "port", 8080,
		"TODO")
	cmd.Flags().StringVar(&args.PullRequestURL, "pull-request", "",
		"TODO")

	if err := cmd.ExecuteContext(ctx); err != nil {
		dlog.Errorf(ctx, "quit: %v", err)
		os.Exit(1)
	}
}

// Main in mostly mimics pkg/client/userd.run(), but is trimmed down for running in a Pod.
func Main(ctx context.Context, args Args) error {
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

	loginExecutor := PoddLoginExecutor{key: args.CloudAPIKey}

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
	grp.Go("main", func(ctx context.Context) error {
		dlog.Infof(ctx, "Connecting to traffic manager...")
		cResp, err := userdCoreImpl.Connect(ctx, &rpc_userd.ConnectRequest{
			// I don't think we need to set anything here.
			KubeFlags:        nil, // nil should be fine since we're in-cluster
			MappedNamespaces: nil, // we're not doing networking things.
		})
		if err != nil {
			return err
		}
		if err := connectError(cResp); err != nil {
			return err
		}
		dlog.Infof(ctx, "Connected to traffic manager")

		dlog.Infof(ctx, "Creating intercept...")
		iResp, err := userdCoreImpl.CreateIntercept(ctx, &rpc_userd.CreateInterceptRequest{
			Spec: &rpc_manager.InterceptSpec{
				Name:          args.WorkloadName,
				Client:        "", // empty for CreateInterceptRequest
				Agent:         args.WorkloadName,
				WorkloadKind:  args.WorkloadKind,
				Namespace:     args.WorkloadNamespace,
				Mechanism:     "http",
				MechanismArgs: []string{"TODO"},
				TargetHost:    "127.0.0.1",
				TargetPort:    args.Port,
				ServiceName:   args.WorkloadName,
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

		// now just wait to be signaled to shut down
		dlog.Infof(ctx, "Maintaining intercept until shutdown...")
		<-ctx.Done()
		return nil
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
