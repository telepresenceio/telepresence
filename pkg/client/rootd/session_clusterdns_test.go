package rootd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// A cluster test cannot make a traffic-manager send a ClusterInfo without dns or an A record without data.
func TestClusterDNS_missing(t *testing.T) {
	require.NotNil(t, clusterDNS(&manager.ClusterInfo{}))
	d := &manager.DNS{ClusterDomain: "x."}
	require.Same(t, d, clusterDNS(&manager.ClusterInfo{Dns: d}))
}
