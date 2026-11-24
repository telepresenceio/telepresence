package connect

import (
	"context"
	"os/exec"
	"strings"
)

// ipOutput runs `ip` with the args and returns the output. A failing ip, or no
// ip at all, yields "" so that callers see an empty table.
func ipOutput(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, "ip", args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// routesAround returns the lines of `ip route show table all` whose destination is
// exactly addr and that do not use a Telepresence device, ignoring the kernel's
// "local" and "broadcast" entries. Such a route sends traffic for addr out
// through the workstation's own network.
func routesAround(ctx context.Context, addr string) []string {
	var lines []string
	for _, family := range []string{"-4", "-6"} {
		for _, line := range strings.Split(ipOutput(ctx, family, "route", "show", "table", "all"), "\n") {
			f := strings.Fields(line)
			if len(f) == 0 || f[0] == "local" || f[0] == "broadcast" {
				continue
			}
			if dst := strings.TrimSuffix(strings.TrimSuffix(f[0], "/32"), "/128"); dst == addr && !strings.Contains(line, "dev tel") {
				lines = append(lines, strings.TrimSpace(line))
			}
		}
	}
	return lines
}
