package fwd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestUDPDispatch_HTTPFilters_NotHandled(t *testing.T) {
	f := &udp{interceptor: &interceptor{
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{
		HeaderFilters: map[string]string{"X-Test": "value"},
	}}
	f.SetIntercepting([]*manager.InterceptInfo{intercept})
	handled := f.IsHTTP()
	require.False(t, handled, "UDP dispatch should not handle HTTP filters")
}

func TestUDPDispatch_NoMechanism_NotHandled(t *testing.T) {
	f := &udp{interceptor: &interceptor{
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}
	f.SetIntercepting(nil)
	handled := f.IsHTTP()
	require.False(t, handled)
}
