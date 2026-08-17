package trafficmgr

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// fakeResolvePortManagerClient embeds the (nil) manager.ManagerClient interface and
// overrides only ResolveServicePort, so tests don't have to stub the whole interface.
type fakeResolvePortManagerClient struct {
	manager.ManagerClient
	rsp *manager.ResolveServicePortResponse
	err error
}

func (f *fakeResolvePortManagerClient) ResolveServicePort(
	context.Context, *manager.ResolveServicePortRequest, ...grpc.CallOption,
) (*manager.ResolveServicePortResponse, error) {
	return f.rsp, f.err
}

// configContext returns a context carrying the default client config with the
// given manager address.
func configContext(t *testing.T, managerAddress string) context.Context {
	cfg := client.GetDefaultConfig()
	cfg.Cluster().ManagerAddress = managerAddress
	return client.WithConfig(t.Context(), cfg)
}

func TestResolveServicePort(t *testing.T) {
	si := &manager.SessionInfo{SessionId: "s"}
	ip := netip.MustParseAddr("10.0.0.42")
	ipb, err := ip.MarshalBinary()
	require.NoError(t, err)

	t.Run("resolved by the manager", func(t *testing.T) {
		fc := &fakeResolvePortManagerClient{rsp: &manager.ResolveServicePortResponse{ClusterIp: ipb, Port: 8080}}
		ap, err := resolveServicePort(t.Context(), fc, si, "default", "echo", "http", types.ProtoTCP)
		require.NoError(t, err)
		assert.Equal(t, netip.AddrPortFrom(ip, 8080), ap.AddrPort)
		assert.Equal(t, types.ProtoTCP, ap.Proto)
	})

	t.Run("unimplemented manager, external mode reports a user error", func(t *testing.T) {
		fc := &fakeResolvePortManagerClient{err: status.Error(codes.Unimplemented, "no such method")}
		ctx := configContext(t, "tls://tm.example.com:8443")
		_, err := resolveServicePort(ctx, fc, si, "default", "echo", "http", types.ProtoTCP)
		require.Error(t, err)
		assert.ErrorContains(t, err, "numeric port")
		assert.Equal(t, errcat.User, errcat.GetCategory(err))
	})

	t.Run("unimplemented manager falls back to a direct Kubernetes lookup", func(t *testing.T) {
		svc := &core.Service{
			ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default"},
			Spec: core.ServiceSpec{
				ClusterIP: "10.0.0.42",
				Ports:     []core.ServicePort{{Name: "http", Port: 8080, Protocol: core.ProtocolTCP}},
			},
		}
		ctx := k8sapi.WithK8sInterface(configContext(t, ""), fake.NewSimpleClientset(svc))
		fc := &fakeResolvePortManagerClient{err: status.Error(codes.Unimplemented, "no such method")}
		ap, err := resolveServicePort(ctx, fc, si, "default", "echo", "http", types.ProtoTCP)
		require.NoError(t, err)
		assert.Equal(t, netip.AddrPortFrom(ip, 8080), ap.AddrPort)
		assert.Equal(t, types.ProtoTCP, ap.Proto)
	})

	t.Run("other manager errors propagate unchanged", func(t *testing.T) {
		wantErr := errors.New("boom")
		fc := &fakeResolvePortManagerClient{err: wantErr}
		_, err := resolveServicePort(t.Context(), fc, si, "default", "echo", "http", types.ProtoTCP)
		require.Error(t, err)
		assert.Equal(t, wantErr, err)
	})
}
