package bwcompat

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func FixLegacyClusterInfo(mgrInfo *manager.ClusterInfo) {
	if len(mgrInfo.ServiceCidrs) == 0 && mgrInfo.ServiceSubnet != nil {
		// Older client with no support for multiple service subnets
		cidr := iputil.RPCToPrefix(mgrInfo.ServiceSubnet)
		sb, _ := cidr.MarshalBinary()
		mgrInfo.ServiceCidrs = [][]byte{sb}
	}
}
