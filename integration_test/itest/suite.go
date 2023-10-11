package itest

import (
	"context"

	"github.com/stretchr/testify/suite"
)

type TestingSuite interface {
	suite.TestingSuite
	Harness
	Context() context.Context
	Assert() *Assertions
	Require() *Requirements
	SuiteName() string
}

type Suite struct {
	suite.Suite
	Harness
}

func (s *Suite) Context() context.Context {
	return WithT(s.HarnessContext(), s.T())
}

func (s *Suite) Assert() *Assertions {
	return &Assertions{Assertions: s.Suite.Assert()}
}

func (s *Suite) Require() *Requirements {
	return &Requirements{Assertions: s.Suite.Require()}
}
