package server

import (
	"context"
	"time"
)

func NewCombinedContext(a, b context.Context) context.Context {
	return &combinedContext{a, b, nil}
}

type combinedContext struct {
	a   context.Context
	b   context.Context
	err error
}

func (c *combinedContext) Deadline() (time.Time, bool) {
	if dla, ok := c.a.Deadline(); ok {
		if dlb, ok := c.b.Deadline(); ok {
			if dlb.Before(dla) {
				return dlb, ok
			}
		}
		return dla, ok
	}
	return c.b.Deadline()
}

func (c *combinedContext) Done() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		select {
		case <-c.a.Done():
			c.err = c.a.Err()
		case <-c.b.Done():
			c.err = c.b.Err()
		}
		close(done)
	}()
	return done
}

func (c *combinedContext) Err() error {
	return c.err
}

func (c *combinedContext) Value(key any) any {
	v := c.a.Value(key)
	if v == nil {
		v = c.b.Value(key)
	}
	return v
}
