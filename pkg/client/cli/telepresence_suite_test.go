package cli_test

import (
	"io/ioutil"
	"log"
	"os"
	"testing"

	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"

	"github.com/datawire/ambassador/pkg/dtest"
)

func TestTelepresence(t *testing.T) {
	RegisterFailHandler(Fail)

	// Remove very verbose output from DTEST initialization
	log.SetOutput(ioutil.Discard)

	config.DefaultReporterConfig.SlowSpecThreshold = 20
	dtest.WithMachineLock(func() {
		_ = os.Chdir("../../..")
		RunSpecs(t, "Telepresence Suite")
	})
}
