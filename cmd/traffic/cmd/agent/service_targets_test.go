package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestAdvertisedInterceptTargets(t *testing.T) {
	ac := &agentconfig.Sidecar{Containers: []*agentconfig.Container{{
		Name: "app",
		Intercepts: []*agentconfig.Intercept{
			{
				ServiceUID:      k8sTypes.UID("shared-service"),
				ServiceName:     "app",
				ServicePortName: "http",
				ServicePort:     80,
				Protocol:        types.ProtoTCP,
				ContainerPort:   8080,
			},
			{
				// Duplicates are intentionally omitted from AgentInfo.
				ServiceUID:      k8sTypes.UID("shared-service"),
				ServiceName:     "app",
				ServicePortName: "http",
				ServicePort:     80,
				Protocol:        types.ProtoTCP,
				ContainerPort:   8080,
			},
			{
				// Container-only targets are still workload-scoped.
				Protocol:      types.ProtoTCP,
				ContainerPort: 9090,
			},
		},
	}}}

	require.Equal(t, []*rpc.AgentInfo_InterceptTarget{{
		ServiceUid:      "shared-service",
		ServiceName:     "app",
		ServicePortName: "http",
		ServicePort:     80,
		Protocol:        "TCP",
		ContainerName:   "app",
		ContainerPort:   8080,
	}}, advertisedInterceptTargets(ac))
}
