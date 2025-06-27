package integration_test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *notConnectedSuite) Test_AlsoProxy32() {
	const ipToTest = "10.10.74.1"
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--also-proxy", ipToTest+"/32", "--name", "ax")
	itest.TelepresenceOk(ctx, "loglevel", "trace")
	defer itest.TelepresenceQuitOk(ctx)
	defer itest.TelepresenceOk(ctx, "loglevel", "debug")

	rq := s.Require()
	logFile := filepath.Join(filelocation.AppUserLogDir(s.Context()), "daemon.log")
	rootLog, err := os.Open(logFile)
	rq.NoError(err)
	defer rootLog.Close()

	// Figure out where the current end of the logfile is. This must be done before any
	// of the tests run because the queries that the DNS resolver receives are dependent
	// on how the system's DNS resolver handles search paths and caching.
	st, err := rootLog.Stat()
	rq.NoError(err)
	pos := st.Size()

	// Make an attempt to curl the also-proxied IP. The attempt will fail (there's nothing at the
	// other end), and that's OK. We're just interested in seeing it logged.
	_, _ = itest.Output(ctx, "curl", "--silent", "--max-time", "1", ipToTest) //nolint:dogsled // X

	// Verify that the attempt is visible in the root log.
	_, err = rootLog.Seek(pos, io.SeekStart)
	rq.NoError(err)
	scn := bufio.NewScanner(rootLog)
	found := false

	// mustHaveWanted caters for cases where the default behavior from the system's resolver
	// is to not send unwanted queries to our resolver at all (based on search and routes).
	// It is forced to true for inclusion tests.
	strToFind := fmt.Sprintf("%s:80, code STREAM_INFO", ipToTest)
	for scn.Scan() {
		txt := scn.Text()
		if strings.Contains(txt, strToFind) {
			found = true
			break
		}
	}
	s.Truef(found, "Unable to find %q", strToFind)
}
