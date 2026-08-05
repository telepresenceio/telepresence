package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestContainerForInterceptUsesMatchedServiceTarget(t *testing.T) {
	fs := &fwdState{
		intercept: agentconfig.NewInterceptTarget([]*agentconfig.Intercept{{
			ServiceUID:    k8sTypes.UID("service-uid"),
			ServicePort:   80,
			Protocol:      types.ProtoTCP,
			ContainerPort: 8080,
		}}),
		container: "canary-app",
	}

	require.Equal(t, "canary-app", fs.containerForIntercept(&rpc.InterceptSpec{
		ServiceUid:    "service-uid",
		ServicePort:   80,
		Protocol:      "TCP",
		ContainerName: "primary-app",
	}))
	require.Equal(t, "primary-app", fs.containerForIntercept(&rpc.InterceptSpec{
		ContainerName: "primary-app",
	}))
}
