// +build darwin

package nat

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
	err := pf([]string{"-F", "all"}, "")
	if err != nil {
		panic(err)
	}
	err = pf([]string{"-f", "/dev/stdin"}, e.pfconf)
	if err != nil {
		panic(err)
	}
}

func (e *env) teardown() {
	pf([]string{"-F", "all"}, "")
}
