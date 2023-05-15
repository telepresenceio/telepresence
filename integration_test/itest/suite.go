package itest

import (
	"context"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type TestingSuite interface {
	suite.TestingSuite
	Harness
	Context() context.Context
	Assert() *assert.Assertions
	Require() *require.Assertions
	SuiteName() string
}

type Suite struct {
	suite.Suite
	Harness
}

func (s *Suite) Context() context.Context {
	return WithT(s.HarnessContext(), s.T())
}
