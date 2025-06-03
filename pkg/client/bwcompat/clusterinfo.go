package bwcompat

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func FixLegacyClusterInfo(mgrInfo *manager.ClusterInfo) {
	// Older clients use index 1 in the ClusterInfo to pass the kube-dns IP. It
	// will manifest itself as one entry of len 4 in the servicecidrs.
	if len(mgrInfo.ServiceCidrs) == 1 && len(mgrInfo.ServiceCidrs[0]) == 4 {
		// Older client with no support for multiple service subnets
		if mgrInfo.ServiceSubnet != nil {
			cidr := iputil.RPCToPrefix(mgrInfo.ServiceSubnet)
			sb, _ := cidr.MarshalBinary()
			mgrInfo.ServiceCidrs = [][]byte{sb}
		} else {
			mgrInfo.ServiceCidrs = nil
		}
	}
}
