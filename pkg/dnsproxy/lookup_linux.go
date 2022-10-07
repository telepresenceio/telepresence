package dnsproxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func externalLookup(ctx context.Context, host string, timeout time.Duration) iputil.IPs {
	cmd := proc.CommandContext(ctx, "nslookup", fmt.Sprintf("-timeout=%.2g", timeout.Seconds()), host)
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
		if a := strings.TrimPrefix(s, "Name:\t"); a != s && strings.HasPrefix(a, host) && sc.Scan() {
			s = sc.Text()
			if a := strings.TrimPrefix(s, "Address:"); a != s {
				if ip := iputil.Parse(strings.TrimSpace(a)); ip != nil {
					ips = append(ips, ip)
				}
			}
		}
	}
	return ips
}
