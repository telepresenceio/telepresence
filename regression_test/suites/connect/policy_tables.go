package connect

import (
	"context"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// firstTelepresenceTable is the first routing table number that the root
// daemon's Linux router uses for its policy rules (pkg/routing openTable).
const firstTelepresenceTable = 775

var ruleTable = regexp.MustCompile(`lookup (\d+)`)

// routingBaseline is the policy routing state that existed before a test
// connected. Everything the test checks or removes is limited to what
// appeared after it.
type routingBaseline struct {
	rules  map[string]bool
	tables map[int]bool
}

// ruleLines returns the policy rule lines of the family that send lookups to a
// table numbered firstTelepresenceTable or higher, keyed by the trimmed line.
func ruleLines(ctx context.Context, family string) map[string]int {
	lines := map[string]int{}
	for _, line := range strings.Split(ipOutput(ctx, family, "rule", "show"), "\n") {
		for _, m := range ruleTable.FindAllStringSubmatch(line, -1) {
			if n, _ := strconv.Atoi(m[1]); n >= firstTelepresenceTable {
				lines[family+" "+strings.TrimSpace(line)] = n
			}
		}
	}
	return lines
}

// snapshotRouting records the policy rules and tables that exist now.
func snapshotRouting(ctx context.Context) routingBaseline {
	b := routingBaseline{rules: map[string]bool{}, tables: map[int]bool{}}
	for _, family := range []string{"-4", "-6"} {
		for line, n := range ruleLines(ctx, family) {
			b.rules[line] = true
			b.tables[n] = true
		}
	}
	return b
}

// currentTables returns the Telepresence-range tables that policy rules send
// lookups to now.
func currentTables(ctx context.Context) []int {
	var tables []int
	for n := range snapshotRouting(ctx).tables {
		tables = append(tables, n)
	}
	sort.Ints(tables)
	return tables
}

// newRules returns the rule lines that were not in the baseline, and the
// tables they send lookups to, excluding tables that were in the baseline.
func (b routingBaseline) newRules(ctx context.Context) (rules []string, tables []int) {
	seen := map[int]bool{}
	for _, family := range []string{"-4", "-6"} {
		for line, n := range ruleLines(ctx, family) {
			if b.rules[line] {
				continue
			}
			rules = append(rules, line)
			if !b.tables[n] && !seen[n] {
				seen[n] = true
				tables = append(tables, n)
			}
		}
	}
	sort.Strings(rules)
	sort.Ints(tables)
	return rules, tables
}

// leftoverRouting describes the policy rules that appeared after the baseline
// and the routes in the given tables. It returns "" when nothing is left.
func (b routingBaseline) leftoverRouting(ctx context.Context, tables ...int) string {
	var sb strings.Builder
	rules, _ := b.newRules(ctx)
	for _, r := range rules {
		sb.WriteString("rule: " + r + "\n")
	}
	for _, family := range []string{"-4", "-6"} {
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

// removeLeftoverRouting deletes policy rules and flushes routes for tables that
// appeared after the baseline, using sudo. Tables from the baseline are never
// touched.
func (b routingBaseline) removeLeftoverRouting(ctx context.Context, tables ...int) {
	for _, family := range []string{"-4", "-6"} {
		for _, table := range tables {
			if b.tables[table] {
				continue
			}
			t := strconv.Itoa(table)
			for strings.Contains(ipOutput(ctx, family, "rule", "show"), "lookup "+t) {
				if exec.CommandContext(ctx, "sudo", "-n", "ip", family, "rule", "del", "lookup", t).Run() != nil {
					break
				}
			}
			_ = exec.CommandContext(ctx, "sudo", "-n", "ip", family, "route", "flush", "table", t).Run()
		}
	}
}
