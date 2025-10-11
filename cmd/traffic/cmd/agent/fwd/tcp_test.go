package fwd

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestTCPDispatch_HTTPMechanism_Handled(t *testing.T) {
	// Create a minimal tcp interceptor instance
	f := &tcp{interceptor: &interceptor{
		lCtx:    context.Background(),
		lCancel: func() {},
	}}

	// net.Pipe gives us a pair of in-memory connections
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Close the server side immediately to cause EOF on read
	serverConn.Close()

	// Minimal intercept info with HTTP filters (enables HTTP mechanism)
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{
		HeaderFilters: map[string]string{"X-Test": "value"},
	}}

	// Set the HTTP intercepts (simulates what fwdstate.HandlePort does)
	f.SetInterceptingMultiple(context.Background(), []*manager.InterceptInfo{intercept})

	// Call dispatch. Since the connection is closed, the HTTP handler
	// will get EOF when trying to read and return an error.
	handled, err := f.DispatchByMechanism(context.Background(), clientConn, intercept)
	require.True(t, handled, "expected HTTP mechanism to be handled by TCP dispatch")
	require.Error(t, err, "expected an error due to EOF on closed connection")
}

func TestTCPDispatch_NoMechanism_NotHandled(t *testing.T) {
	f := &tcp{interceptor: &interceptor{
		lCtx:    context.Background(),
		lCancel: func() {},
	}}

	// No intercept
	handled, err := f.DispatchByMechanism(context.Background(), nil, nil)
	require.False(t, handled)
	require.NoError(t, err)

	// Intercept without HTTP mechanism (no filters)
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{}}
	handled, err = f.DispatchByMechanism(context.Background(), nil, intercept)
	require.False(t, handled)
	require.NoError(t, err)
}
