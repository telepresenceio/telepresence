package agent_test

import (
	"context"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/fwd"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

const (
	appPort uint16 = 5000
)

var appTarget = netip.AddrPortFrom(netip.MustParseAddr("192.168.1.100"), appPort)

func makeFS(t *testing.T, ctx context.Context) (fwd.Interceptor, agent.State) {
	ctx, cancel := context.WithCancel(ctx)
	f := fwd.NewInterceptor(ctx, types.PortAndProto{Proto: types.ProtoTCP, Port: 1111}, tunnel.AgentToProxied, appTarget)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := f.Serve(ctx, nil); err != nil {
			clog.Error(ctx, err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	c, err := agent.LoadConfig(ctx)
	require.NoError(t, err)
	s, err := agent.NewState(ctx, c)
	require.NoError(t, err)
	cn := c.AgentConfig().Containers[0]
	cnMountPoint := filepath.Join(agentconfig.ExportsMountPoint, filepath.Base(cn.MountPoint))
	s.AddContainerState(cn.Name, s.NewContainerState(s, cn, cnMountPoint, map[string]string{}))
	s.AddInterceptState(s.NewInterceptState(f, agentconfig.NewInterceptTarget(cn.Intercepts), cn.Name))
	return f, s
}

func TestState_HandleIntercepts(t *testing.T) {
	ctx := testContext(t, nil)
	a := assert.New(t)
	_, s := makeFS(t, ctx)

	var (
		cepts   []*rpc.InterceptInfo
		reviews []*rpc.ReviewInterceptRequest
	)

	// Handle resets state on an empty intercept list

	reviews = s.HandleIntercepts(ctx, cepts)
	a.Len(reviews, 0)

	// Prepare some intercepts..

	cepts = []*rpc.InterceptInfo{
		{
			Spec: &rpc.InterceptSpec{
				Name:           "cept1Name",
				Client:         "user@host1",
				Agent:          "agentName",
				Mechanism:      "tcp",
				Namespace:      namespace,
				ServiceName:    serviceName,
				PortIdentifier: "http",
				ContainerPort:  8080,
				Protocol:       string(core.ProtocolTCP),
				TargetPort:     8080,
			},
			Id: "intercept-01",
		},
		{
			Spec: &rpc.InterceptSpec{
				Name:           "cept2Name",
				Client:         "user@host2",
				Agent:          "agentName",
				Mechanism:      "tcp",
				Namespace:      namespace,
				ServiceName:    serviceName,
				PortIdentifier: "http",
				ContainerPort:  8080,
				Protocol:       string(core.ProtocolTCP),
				TargetPort:     8080,
			},
			Id: "intercept-02",
		},
	}

	// Handle ignores non-active and non-waiting intercepts

	cepts[0].Disposition = rpc.InterceptDispositionType_NO_PORTS
	cepts[1].Disposition = rpc.InterceptDispositionType_NO_CLIENT

	reviews = s.HandleIntercepts(ctx, cepts)
	a.Len(reviews, 0)

	// Handle reviews waiting intercepts

	cepts[0].Disposition = rpc.InterceptDispositionType_WAITING
	cepts[1].Disposition = rpc.InterceptDispositionType_WAITING

	reviews = s.HandleIntercepts(ctx, cepts)
	require.Len(t, reviews, 2)

	// Reviews are in the correct order

	a.Equal(cepts[0].Id, reviews[0].Id)
	a.Equal(cepts[1].Id, reviews[1].Id)

	// First cept was accepted, second was rejected

	a.Equal(rpc.InterceptDispositionType_ACTIVE, reviews[0].Disposition)
	a.Equal(rpc.InterceptDispositionType_AGENT_ERROR, reviews[1].Disposition)
	a.Equal(`conflicts with the currently waiting intercept "intercept-01" ("user@host1"): one intercept has no filters (intercepts all traffic)`, reviews[1].Message)

	// Handle conflicts

	cepts[0].Disposition = rpc.InterceptDispositionType_ACTIVE
	cepts[1].Disposition = rpc.InterceptDispositionType_WAITING

	reviews = s.HandleIntercepts(ctx, cepts)
	a.Len(reviews, 1)
	a.Equal(cepts[1].Id, reviews[0].Id)

	a.Equal(rpc.InterceptDispositionType_AGENT_ERROR, reviews[0].Disposition)
	a.Equal(`conflicts with the currently active intercept "intercept-01" ("user@host1"): one intercept has no filters (intercepts all traffic)`, reviews[0].Message)

	// Handle resets state on an empty intercept list again

	reviews = s.HandleIntercepts(ctx, nil)
	a.Len(reviews, 0)
}
