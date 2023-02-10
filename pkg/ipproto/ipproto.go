// Package ipproto contains IP protocol numbers
// https://www.iana.org/assignments/protocol-numbers/protocol-numbers.xhtml
package ipproto

import "fmt"

const (
	TCP    = 6
	UDP    = 17
	ICMP   = 1
	ICMPV6 = 58
)

// Parse returns the IP protocol for the given network. Currently only supports
// TCP, UDP, and ICMP.
func Parse(network string) int {
	switch network {
	case "tcp", "tcp4":
		return TCP
	case "udp", "udp4", "udp6":
		return UDP
	case "icmp":
		return ICMP
	case "icmpv6":
		return ICMPV6
	default:
		return -1
	}
}

func String(proto int) string {
	switch proto {
	case ICMP:
		return "icmp"
	case TCP:
		return "tcp"
	case UDP:
		return "udp"
	case ICMPV6:
		return "icmpv6"
	default:
		return fmt.Sprintf("IP-protocol %d", proto)
	}
}
