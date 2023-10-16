package itest

import (
	"context"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Requirements struct {
	*require.Assertions
}

func (r *Requirements) EventuallyContext(ctx context.Context, condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...any) {
	r.Eventually(func() bool {
		if ctx.Err() != nil {
			return true
		}
		return condition()
	}, waitFor, tick, msgAndArgs...)
	r.NoError(ctx.Err())
}

type Assertions struct {
	*assert.Assertions
}

func (r *Assertions) EventuallyContext(ctx context.Context, condition func() bool, waitFor time.Duration, tick time.Duration, msgAndArgs ...any) bool {
	return r.Eventually(func() bool {
		if ctx.Err() != nil {
			return true
		}
		return condition()
	}, waitFor, tick, msgAndArgs...) || r.NoError(ctx.Err())
}
