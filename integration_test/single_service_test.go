package integration_test

import (
	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type singleServiceSuite struct {
	itest.Suite
	itest.SingleService
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) suite.TestingSuite {
		return &singleServiceSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}
