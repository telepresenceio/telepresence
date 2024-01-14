package integration_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// Test_WpadNotForwarded tests that DNS request aren't forwarded
// to the cluster.
func (s *connectedSuite) Test_WpadNotForwarded() {
	ctx := s.Context()
	logFile := filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log")

	tests := []struct {
		qn      string
		forward bool
	}{
		{
			"wpad",
			false,
		},
		{
			fmt.Sprintf("wpad.%s", s.AppNamespace()),
			false,
		},
		{
			"wpad.cluster.local",
			false,
		},
		{
			"wpad.svc.cluster.local",
			false,
		},
		{
			fmt.Sprintf("wpad.%s.svc.cluster.local", s.AppNamespace()),
			false,
		},
		/* revisit after checking relevant log messages on all platforms
		{
			"wpad.bogus.nu",
			true,
		},
		*/
	}

	// Figure out where the current end of the logfile is. This must be done before any
	// of the tests run because the queries that the DNS resolver receives are dependent
	// on how the system's DNS resolver handle search paths and caching.
	st, err := os.Stat(logFile)
	s.Require().NoError(err)
	pos := st.Size()

	for _, tt := range tests {
		s.Run(tt.qn, func() {
			require := s.Require()
			ctx := s.Context()

			// Make an attempt to resolve the host
			short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			_, _ = net.DefaultResolver.LookupIPAddr(short, tt.qn)
			dtime.SleepWithContext(ctx, 200*time.Millisecond)

			// Seek to the end of the log as it were before the lookup
			rootLog, err := os.Open(logFile)
			require.NoError(err)
			defer rootLog.Close()
			_, err = rootLog.Seek(pos, 0)
			require.NoError(err)

			// Ensure that there's an A record with an NXDOMAIN but no LookupHost call
			// with a "wpad." prefix. The host may not match exactly due to how the
			// OS handles search paths.
			hasNX := false
			hasLookup := false
			scn := bufio.NewScanner(rootLog)
			for scn.Scan() {
				txt := scn.Text()
				if strings.Contains(txt, "wpad") {
					if !hasLookup {
						if s.IsIPv6() {
							hasLookup = strings.Contains(txt, "Lookup AAAA ")
						} else {
							hasLookup = strings.Contains(txt, "Lookup A ")
						}
					}
					if !hasNX {
						hasNX = strings.Contains(txt, "-> NXDOMAIN")
					}
				}
			}
			if tt.qn == "wpad" && !hasNX && !hasLookup {
				// this is very likely OK because our DNS server never received the request. It
				// was filtered by the OS DNS framework. Those tests are only relevant when the overriding
				// DNS resolver is used.
				return
			}
			if tt.forward {
				require.Truef(hasLookup, "Missing expected Lookup A log for %s", tt.qn)
			} else {
				require.Falsef(hasLookup, "Found unexpected Lookup A log for %s", tt.qn)
				require.Truef(hasNX, "No NXDOMAIN record found for %s", tt.qn)
			}
		})
	}
}
