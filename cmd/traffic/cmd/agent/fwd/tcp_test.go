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
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
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
	f.SetIntercepting([]*manager.InterceptInfo{intercept})

	// Call dispatch. Since the connection is closed, the HTTP handler
	// will get EOF when trying to read and return an error.
	handled := f.IsHTTP()
	require.True(t, handled, "expected HTTP mechanism to be handled by TCP dispatch")
}

func TestTCPDispatch_NoMechanism_NotHandled(t *testing.T) {
	f := &tcp{interceptor: &interceptor{
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}

	// No intercept
	handled := f.IsHTTP()
	require.False(t, handled)

	// Intercept without HTTP mechanism (no filters)
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{}}
	f.SetIntercepting([]*manager.InterceptInfo{intercept})
	handled = f.IsHTTP()
	require.False(t, handled)
}
