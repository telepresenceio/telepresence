package forwarder

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestUDPDispatch_HTTPFilters_NotHandled(t *testing.T) {
	f := &udp{interceptor: interceptor{}}
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{
		HeaderFilters: map[string]string{"X-Test": "value"},
	}}

	handled, err := f.DispatchByMechanism(context.Background(), nil, intercept)
	require.False(t, handled, "UDP dispatch should not handle HTTP filters")
	require.NoError(t, err)
}

func TestUDPDispatch_NoMechanism_NotHandled(t *testing.T) {
	f := &udp{interceptor: interceptor{}}
	handled, err := f.DispatchByMechanism(context.Background(), nil, nil)
	require.False(t, handled)
	require.NoError(t, err)
}
