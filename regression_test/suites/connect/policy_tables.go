package connect

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// firstTelepresenceTable is the first routing table number that the root
// daemon's Linux router uses for its policy rules (pkg/routing openTable).
const firstTelepresenceTable = 775

var ruleTable = regexp.MustCompile(`lookup (\d+)`)

// telepresenceTables returns the numbers of the routing tables that policy rules
// (IPv4 and IPv6) send lookups to and that the root daemon's router owns.
func telepresenceTables(ctx context.Context) []int {
	var tables []int
	seen := map[int]bool{}
	for _, family := range []string{"-4", "-6"} {
		for _, m := range ruleTable.FindAllStringSubmatch(ipOutput(ctx, family, "rule", "show"), -1) {
			n, _ := strconv.Atoi(m[1])
			if n >= firstTelepresenceTable && !seen[n] {
				seen[n] = true
				tables = append(tables, n)
			}
		}
	}
	return tables
}

// leftoverRouting describes policy rules that point at a table numbered
// firstTelepresenceTable or higher, and routes in the given tables. It
// returns "" when nothing is left.
func leftoverRouting(ctx context.Context, tables ...int) string {
	var sb strings.Builder
	for _, family := range []string{"-4", "-6"} {
		for _, line := range strings.Split(ipOutput(ctx, family, "rule", "show"), "\n") {
			for _, m := range ruleTable.FindAllStringSubmatch(line, -1) {
				if n, _ := strconv.Atoi(m[1]); n >= firstTelepresenceTable {
					sb.WriteString("rule: " + strings.TrimSpace(line) + "\n")
				}
			}
		}
		for _, table := range tables {
			for _, line := range strings.Split(ipOutput(ctx, family, "route", "show", "table", strconv.Itoa(table)), "\n") {
				if strings.TrimSpace(line) != "" {
					sb.WriteString("route in table " + strconv.Itoa(table) + ": " + strings.TrimSpace(line) + "\n")
				}
			}
		}
	}
	return sb.String()
}

// removeLeftoverRouting deletes policy rules and flushes the tables, using
// sudo. It is a safety net so that a failed test does not leave the
// workstation with routing state that later connects trip over.
func removeLeftoverRouting(ctx context.Context, tables ...int) {
	for _, family := range []string{"-4", "-6"} {
		for _, table := range tables {
			t := strconv.Itoa(table)
			for ipOutput(ctx, family, "rule", "show") != "" && strings.Contains(ipOutput(ctx, family, "rule", "show"), "lookup "+t) {
				if exec.CommandContext(ctx, "sudo", "-n", "ip", family, "rule", "del", "lookup", t).Run() != nil {
					break
				}
			}
			_ = exec.CommandContext(ctx, "sudo", "-n", "ip", family, "route", "flush", "table", t).Run()
		}
	}
}
