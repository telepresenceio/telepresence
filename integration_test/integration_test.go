package integration_test

import (
	"path/filepath"
	"testing"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func Test_Integration(t *testing.T) {
	ossRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("unable to get absolute path of .oss: %v", err)
	}
	itest.RunTests(itest.TestContext(t, ossRoot, filepath.Dir(ossRoot)))
}
