// +build !windows

package ipproto

import "golang.org/x/sys/unix"

const (
	TCP    = unix.IPPROTO_TCP
	UDP    = unix.IPPROTO_UDP
	ICMP   = unix.IPPROTO_ICMP
	ICMPV6 = unix.IPPROTO_ICMPV6
)
