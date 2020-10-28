// +build linux

package nat

// we don't yet have any iptables config cases to test against

type env struct{}

var environments = []env{
	{},
}

func (e *env) setup() {}

func (e *env) teardown() {}
