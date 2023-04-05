package integration_test

import (
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type singleServiceSuite struct {
	itest.Suite
	itest.SingleService
}

func (s *singleServiceSuite) SuiteName() string {
	return "SingleService"
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &singleServiceSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}
