package itest

import (
	"context"

	"github.com/stretchr/testify/suite"
)

type Suite struct {
	suite.Suite
	Harness
}

func (s *Suite) Context() context.Context {
	return withT(s.HarnessContext(), s.T())
}
