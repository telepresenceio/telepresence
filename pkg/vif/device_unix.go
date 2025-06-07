//go:build !windows
// +build !windows

package vif

import (
	"context"
	"net/netip"
)

func (d *device) setDNS(context.Context, string, netip.AddrPort, []string) (err error) {
	// DNS is configured by other means than through the actual device
	return nil
}
