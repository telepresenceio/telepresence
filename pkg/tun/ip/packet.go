package ip

import (
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
)

type Packet interface {
	IPHeader() Header
	Data() *buffer.Data
	SoftRelease()
	Release()
	SetDataAndIPHeader(*buffer.Data, Header)
}

func InitPacket(pkg Packet, ipPayloadLen int, src, dst net.IP) {
	if len(src) == 4 && len(dst) == 4 {
		data := buffer.DataPool.Get(ipPayloadLen + ipv4.HeaderLen)
		iph := V4Header(data.Buf())
		pkg.SetDataAndIPHeader(data, iph)
		iph.Initialize()
		iph.SetID(NextID())
	} else {
		data := buffer.DataPool.Get(ipPayloadLen + ipv6.HeaderLen)
		iph := V6Header(data.Buf())
		pkg.SetDataAndIPHeader(data, iph)
		iph.Initialize()
	}
	iph := pkg.IPHeader()
	iph.SetSource(src)
	iph.SetDestination(dst)
	iph.SetTTL(64)
	iph.SetPayloadLen(ipPayloadLen)
}
