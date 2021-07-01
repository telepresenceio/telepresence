package ipproto

import "syscall"

const (
	TCP    = syscall.IPPROTO_TCP
	UDP    = syscall.IPPROTO_UDP
	ICMP   = 0x1
	ICMPV6 = 0x3a
)
