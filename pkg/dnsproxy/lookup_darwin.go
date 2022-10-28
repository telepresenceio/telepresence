package dnsproxy

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func externalLookup(ctx context.Context, host string, timeout time.Duration) iputil.IPs {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, "dscacheutil", "-q", "host", "-a", "name", host)
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	// Look for the adjacent lines
	//   Name: <host>
	//   Address: <ip>
	sc := bufio.NewScanner(bytes.NewReader(out))
	var ips iputil.IPs
	for sc.Scan() {
		s := sc.Text()
		if a := strings.TrimPrefix(s, "name: "); a != s && strings.HasPrefix(a, host) && sc.Scan() {
			s = sc.Text()
			if a := strings.TrimPrefix(s, "ip_address: "); a != s {
				if ip := iputil.Parse(a); ip != nil {
					ips = append(ips, ip)
				}
			} else if a := strings.TrimPrefix(s, "ipv6_address: "); a != s {
				if ip := iputil.Parse(a); ip != nil {
					ips = append(ips, ip)
				}
			}
		}
	}
	return ips
}
