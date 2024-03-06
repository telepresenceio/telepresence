package itest

import (
	"context"

	"github.com/stretchr/testify/suite"
)

type TestingSuite interface {
	suite.TestingSuite
	Harness
	AmendSuiteContext(context.Context) context.Context
	Context() context.Context
	Assert() *Assertions
	Require() *Requirements
	SuiteName() string
	setContext(ctx context.Context)
}

type Suite struct {
	suite.Suite
	Harness
	ctx context.Context
}

func (s *Suite) AmendSuiteContext(ctx context.Context) context.Context {
	return ctx
}

//nolint:unused // Linter is confused about this one.
func (s *Suite) setContext(ctx context.Context) {
	s.ctx = ctx
}

func (s *Suite) Context() context.Context {
	return WithT(s.ctx, s.T())
}

func (s *Suite) Assert() *Assertions {
	return &Assertions{Assertions: s.Suite.Assert()}
}

func (s *Suite) Require() *Requirements {
	return &Requirements{Assertions: s.Suite.Require()}
}
