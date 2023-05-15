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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", fmt.Sprintf("(Resolve-DnsName -Name %s -Type A_AAAA -DnsOnly).IPAddress", host))
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var ips iputil.IPs
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if ip := iputil.Parse(strings.TrimSpace(sc.Text())); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}
