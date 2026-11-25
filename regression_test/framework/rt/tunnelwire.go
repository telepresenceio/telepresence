package rt

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/rest"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
)

// TunnelwireRestConfig returns the rest.Config for the run's kubeconfig and context.
func TunnelwireRestConfig(e Env) (*rest.Config, error) {
	return managerRestConfig(e.R)
}

// TunnelwireDialPod opens a gRPC connection to port of the named pod over a Kubernetes
// port-forward, without any credentials.
func TunnelwireDialPod(e Env, ns, pod string, port uint16) (*grpc.ClientConn, error) {
	cfg, err := managerRestConfig(e.R)
	if err != nil {
		return nil, fmt.Errorf("building rest.Config: %w", err)
	}
	ctx := client.WithConfig(e.Ctx, client.GetDefaultConfig())
	ctx = portforward.WithRestConfig(ctx, cfg)
	pa := portforward.PodAddress{Name: pod, Namespace: ns, NoLookup: true}
	return grpcClient.DialGRPC(ctx, portforward.K8sPFScheme+":///"+pa.AddrFor(port),
		grpc.WithResolvers(portforward.NewResolver(ctx)),
		grpc.WithContextDialer(portforward.Dialer(ctx)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}

// TunnelwireContainerState is the restart count and Ready condition of a pod.
type TunnelwireContainerState struct {
	Restarts string
	Ready    string
}

// TunnelwireContainerStates returns, per pod matching selector in ns, the restart count
// of container and the pod's Ready condition.
func TunnelwireContainerStates(ctx context.Context, r *Runtime, ns, selector, container string) (map[string]TunnelwireContainerState, error) {
	jp := `jsonpath={range .items[*]}{.metadata.name}|{.status.containerStatuses[?(@.name=="` + container + `")].restartCount}` +
		`|{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}`
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", selector, "-o", jp)
	if err != nil {
		return nil, err
	}
	states := map[string]TunnelwireContainerState{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) == 3 {
			states[parts[0]] = TunnelwireContainerState{Restarts: parts[1], Ready: parts[2]}
		}
	}
	return states, nil
}
