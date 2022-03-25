package agent_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
)

const (
	appHost        = "appHost"
	appPort uint16 = 5000
)

func makeFS(t *testing.T, ctx context.Context) (*forwarder.Forwarder, agent.State) {
	lAddr, err := net.ResolveTCPAddr("tcp", ":1111")
	assert.NoError(t, err)

	f := forwarder.NewForwarder(lAddr, appHost, appPort)
	go func() {
		if err := f.Serve(context.Background()); err != nil {
			panic(err)
		}
	}()

	assert.Eventually(t, func() bool {
		_, port := f.Target()
		return port == appPort
	}, 1*time.Second, 10*time.Millisecond)

	c, err := agent.LoadConfig(ctx)
	require.NoError(t, err)
	s := agent.NewSimpleState(c)
	cn := c.AgentConfig().Containers[0]
	s.AddInterceptState(agent.NewInterceptState(s, f, cn.Intercepts[0], cn.MountPoint, map[string]string{}))

	return f, s
}

func TestState_HandleIntercepts(t *testing.T) {
	ctx := testContext(t, nil)
	a := assert.New(t)
	f, s := makeFS(t, ctx)

	var (
		host    string
		port    uint16
		cepts   []*rpc.InterceptInfo
		reviews []*rpc.ReviewInterceptRequest
	)

	// Setup worked

	host, port = f.Target()
	a.Equal(appHost, host)
	a.Equal(appPort, port)

	// Handle resets state on an empty intercept list

	reviews = s.HandleIntercepts(ctx, cepts)
	a.Len(reviews, 0)
	a.False(f.Intercepting())

	// Prepare some intercepts..

	cepts = []*rpc.InterceptInfo{
		{
			Spec: &rpc.InterceptSpec{
				Name:                  "cept1Name",
				Client:                "user@host1",
				Agent:                 "agentName",
				Mechanism:             "tcp",
				Namespace:             namespace,
				ServiceName:           serviceName,
				ServicePortIdentifier: "http",
				TargetPort:            8080,
			},
			Id: "intercept-01",
		},
		{
			Spec: &rpc.InterceptSpec{
				Name:                  "cept2Name",
				Client:                "user@host2",
				Agent:                 "agentName",
				Mechanism:             "tcp",
				Namespace:             namespace,
				ServiceName:           serviceName,
				ServicePortIdentifier: "http",
				TargetPort:            8080,
			},
			Id: "intercept-02",
		},
	}

	// Handle ignores non-active and non-waiting intercepts

	cepts[0].Disposition = rpc.InterceptDispositionType_NO_PORTS
	cepts[1].Disposition = rpc.InterceptDispositionType_NO_CLIENT

	reviews = s.HandleIntercepts(ctx, cepts)
	a.Len(reviews, 0)
	a.False(f.Intercepting())

	// Handle reviews waiting intercepts

	cepts[0].Disposition = rpc.InterceptDispositionType_WAITING
	cepts[1].Disposition = rpc.InterceptDispositionType_WAITING

	reviews = s.HandleIntercepts(ctx, cepts)
	a.Len(reviews, 2)
	a.False(f.Intercepting())

	// Reviews are in the correct order

	a.Equal(cepts[0].Id, reviews[0].Id)
	a.Equal(cepts[1].Id, reviews[1].Id)

	// First cept was accepted, second was rejected

	a.Equal(rpc.InterceptDispositionType_ACTIVE, reviews[0].Disposition)
	a.Equal(rpc.InterceptDispositionType_AGENT_ERROR, reviews[1].Disposition)
	a.Equal("Conflicts with the currently-waiting-to-be-served intercept \"intercept-01\"", reviews[1].Message)

	// Handle updates forwarding

	cepts[0].Disposition = rpc.InterceptDispositionType_ACTIVE
	cepts[1].Disposition = rpc.InterceptDispositionType_WAITING

	reviews = s.HandleIntercepts(ctx, cepts)
	a.Len(reviews, 1)
	a.True(f.Intercepting())

	a.Equal(rpc.InterceptDispositionType_AGENT_ERROR, reviews[0].Disposition)
	a.Equal("Conflicts with the currently-served intercept \"intercept-01\"", reviews[0].Message)

	// Handle resets state on an empty intercept list again

	reviews = s.HandleIntercepts(ctx, nil)
	a.Len(reviews, 0)
	a.False(f.Intercepting())
}
