package integration_test

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func Test_Integration(t *testing.T) {
	itest.RunTests(t)
}
