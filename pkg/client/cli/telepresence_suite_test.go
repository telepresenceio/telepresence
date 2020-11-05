package cli_test

import (
	"io/ioutil"
	"log"
	"os"
	"testing"

	"github.com/datawire/ambassador/pkg/dtest"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestTelepresence(t *testing.T) {
	RegisterFailHandler(Fail)
	log.SetOutput(ioutil.Discard)
	dtest.WithMachineLock(func() {
		_ = os.Chdir("../../..")
		RunSpecs(t, "Telepresence Suite")
	})
}
