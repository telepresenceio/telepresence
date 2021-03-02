package nat

import "context"

// we don't yet have any iptables config cases to test against

type env struct{}

var environments = []env{
	{},
}

func (e *env) testName() string { return "" }

func (e *env) setup(_ context.Context) error { return nil }

func (e *env) teardown(_ context.Context) error { return nil }
