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
	cmd := proc.CommandContext(ctx, "nslookup", host)
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	// Look for the adjacent lines
	//   Name: <host> [possibly extended with search path]
	//   Address: <ip>
	var ips iputil.IPs
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		s := sc.Text()
		if a := strings.TrimPrefix(s, "Name:"); a != s && strings.HasPrefix(strings.TrimSpace(a), host) && sc.Scan() {
			s = sc.Text()
			if a := strings.TrimPrefix(s, "Address:"); a != s {
				if ip := iputil.Parse(strings.TrimSpace(a)); ip != nil {
					ips = append(ips, ip)
				}
			} else if a := strings.TrimPrefix(s, "Addresses:"); a != s {
				for {
					if ip := iputil.Parse(strings.TrimSpace(a)); ip != nil {
						ips = append(ips, ip)
					} else {
						break
					}
					if !sc.Scan() {
						break
					}
					a = sc.Text()
				}
			}
		}
	}
	return ips
}
