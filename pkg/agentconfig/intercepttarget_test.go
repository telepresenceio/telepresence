package agentconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestInterceptTargetMatchForServiceSpec(t *testing.T) {
	target := NewInterceptTarget([]*Intercept{{
		ServiceUID:      k8sTypes.UID("service-uid"),
		ServicePortName: "http",
		ServicePort:     80,
		Protocol:        types.ProtoTCP,
		ContainerPort:   8081,
	}})

	require.True(t, target.MatchForSpec(&manager.InterceptSpec{
		ServiceUid:  "service-uid",
		ServicePort: 80,
		Protocol:    "TCP",
		// The primary workload can resolve a different container port.
		ContainerPort: 8080,
	}))
	require.False(t, target.MatchForSpec(&manager.InterceptSpec{
		ServiceUid:  "other-service",
		ServicePort: 80,
		Protocol:    "TCP",
	}))
	require.False(t, target.MatchForSpec(&manager.InterceptSpec{
		ServiceUid:  "service-uid",
		ServicePort: 81,
		Protocol:    "TCP",
	}))
}

func TestInterceptTargetMatchForContainerSpec(t *testing.T) {
	target := NewInterceptTarget([]*Intercept{{
		Protocol:      types.ProtoTCP,
		ContainerPort: 8080,
	}})

	require.True(t, target.MatchForSpec(&manager.InterceptSpec{ContainerPort: 8080, Protocol: "TCP"}))
	require.False(t, target.MatchForSpec(&manager.InterceptSpec{ContainerPort: 8081, Protocol: "TCP"}))
}
