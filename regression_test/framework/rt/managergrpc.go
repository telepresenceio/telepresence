package rt

import (
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// ManagerClient dials the traffic-manager Service in ns (typically
// managers.ManagerNamespace, or a SecondaryManager's own namespace) over a
// port-forward, using the run's kubeconfig/context (RTEST_KUBECONFIG/
// RTEST_CONTEXT via Runtime), and returns a raw manager.ManagerClient plus a
// close func that tears down the underlying gRPC connection. It talks to the
// traffic-manager directly, independent of any `telepresence connect`
// session -- suites use it to assert on manager RPCs (WatchWorkloads,
// GetClusterInfo, ...) a status/list CLI call can't observe directly, or (M4
// compat work) to drive a session against a manager the CLI under test can't
// talk to.
//
// Modeled on integration_test/itest/traffic_manager.go's
// dialTrafficManager: it resolves the traffic-manager Service to a backing
// pod (portforward.ResolveSvcToPod) and dials it through the k8spf://
// resolver scheme (portforward.NewResolver/Dialer), which port-forwards
// under the hood rather than requiring the Service to be otherwise
// reachable.
func ManagerClient(e Env, ns string) (manager.ManagerClient, func(), error) {
	e.T.Helper()
	cfg, err := managerRestConfig(e.R)
	if err != nil {
		return nil, nil, fmt.Errorf("rtest: building rest.Config: %w", err)
	}
	k8sAPI, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("rtest: building kubernetes client: %w", err)
	}

	// The portforward dialer reads the client config from the context and
	// panics without one (streamconn.go's newPodDialer).
	ctx := client.WithConfig(e.Ctx, client.GetDefaultConfig())
	ctx = k8sapi.WithK8sInterface(ctx, k8sAPI)
	ctx = portforward.WithRestConfig(ctx, cfg)
	pap, err := portforward.ResolveSvcToPod(ctx, "traffic-manager", ns, "8081")
	if err != nil {
		return nil, nil, fmt.Errorf("rtest: resolving svc/traffic-manager.%s:8081: %w", ns, err)
	}
	target := fmt.Sprintf("%s:///pod/%s.%s:%d#%s", portforward.K8sPFScheme, pap.Name, pap.Namespace, pap.Port, pap.PodID)
	conn, err := grpcClient.DialGRPC(ctx, target,
		grpc.WithResolvers(portforward.NewResolver(ctx)),
		grpc.WithContextDialer(portforward.Dialer(ctx)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("rtest: dialing traffic-manager.%s: %w", ns, err)
	}
	return manager.NewManagerClient(conn), func() { _ = conn.Close() }, nil
}

// managerRestConfig builds a *rest.Config for the run's kubeconfig/context
// (RTEST_KUBECONFIG/KUBECONFIG, RTEST_CONTEXT), the same inputs Kubectl and
// the CLI's --context flag use, so ManagerClient targets whatever cluster
// and context the rest of the run does.
func managerRestConfig(r *Runtime) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	path := r.kubeconfig
	if path == "" {
		path = os.Getenv("KUBECONFIG")
	}
	if path != "" {
		rules.ExplicitPath = path
	}
	overrides := &clientcmd.ConfigOverrides{}
	if r.kubeCtx != "" {
		overrides.CurrentContext = r.kubeCtx
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}
