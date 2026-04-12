package agentpf

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
)

func TestWaitForIPUnavailableForUnwatchedNamespace(t *testing.T) {
	cl := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   context.Background(),
			Namespace: "alpha",
		},
	}
	cs := NewClients(cl, &manager.SessionInfo{SessionId: "session"}, []string{"alpha"})

	err := cs.WaitForIP(context.Background(), time.Millisecond, "beta", netip.MustParseAddr("10.0.0.1"))
	require.Equal(t, codes.Unavailable, status.Code(err))
}
