// +build darwin

package nat

import "github.com/datawire/teleproxy/pkg/supervisor"

type env struct {
	pfconf string
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
		pf(p, []string{"-F", "all"}, "")
		return nil
	})
}
