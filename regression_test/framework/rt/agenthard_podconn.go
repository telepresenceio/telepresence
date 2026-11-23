package rt

import (
	"context"
	"fmt"
	"net"

	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// AgenthardPodDialer returns a function that opens a TCP connection to a port of a pod
// through the Kubernetes API server's port-forward, using the run's kubeconfig and context.
// The connection reaches the port from inside the pod's network namespace and needs neither
// a traffic-manager nor a telepresence connection.
func AgenthardPodDialer(e Env) (func(ctx context.Context, ns, pod string, port uint16) (net.Conn, error), error) {
	cfg, err := managerRestConfig(e.R)
	if err != nil {
		return nil, fmt.Errorf("rtest: building rest.Config: %w", err)
	}
	k8sAPI, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("rtest: building kubernetes client: %w", err)
	}
	return func(ctx context.Context, ns, pod string, port uint16) (net.Conn, error) {
		pctx := client.WithConfig(e.Ctx, client.GetDefaultConfig())
		pctx = k8sapi.WithK8sInterface(pctx, k8sAPI)
		pctx = portforward.WithRestConfig(pctx, cfg)
		addr := fmt.Sprintf("pod/%s.%s:%d%s%s", pod, ns, port, portforward.UIDSeparator, portforward.NoLookupMarker)
		return portforward.Dialer(pctx)(ctx, addr)
	}, nil
}
