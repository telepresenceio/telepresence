// +build darwin

package nat

import (
	"strings"

	"github.com/datawire/ambassador/pkg/supervisor"
)

type env struct {
	pfconf string
	before string
}

/*
 *  192.0.2.0/24 (TEST-NET-1),
 *  198.51.100.0/24 (TEST-NET-2)
 *  203.0.113.0/24 (TEST-NET-3)
 */

var environments = []env{
	{pfconf: ""},
	{pfconf: "set skip on lo\n"},
	{pfconf: "block return quick proto tcp from any to 192.0.2.0/24\n"},
	{pfconf: "anchor {\n    block return quick proto tcp from any to 192.0.2.0/24\n}\n"},
}

func (e *env) setup() {
	supervisor.MustRun("setup", func(p *supervisor.Process) error {
		output := p.Command("pfctl", "-sr").MustCapture(nil)
		output += p.Command("pfctl", "-sn").MustCapture(nil)
		lines := strings.Split(output, "\n")

		for _, kw := range []string{"scrub-anchor", "nat-anchor", "rdr-anchor", "anchor"} {
			for _, line := range lines {
				fields := strings.Fields(line)
				if len(fields) > 0 && fields[0] == kw {
					e.before += line + "\n"
				}
			}
		}

		err := pf(p, []string{"-F", "all"}, "")
		if err != nil {
			return err
		}
		err = pf(p, []string{"-f", "/dev/stdin"}, e.pfconf)
		if err != nil {
			return err
		}
		return nil
	})
}

func (e *env) teardown() {
	supervisor.MustRun("teardown", func(p *supervisor.Process) error {
		_ = pf(p, []string{"-F", "all"}, "")
		return pf(p, []string{"-f", "/dev/stdin"}, e.before)
	})
}
