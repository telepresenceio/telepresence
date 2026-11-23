package intercept

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgenthardWiretap proves an HTTP wiretap copes with a request that declares a huge body.
type AgenthardWiretap struct {
	rt.Suite
}

func init() {
	rt.Register(&AgenthardWiretap{}, rt.InArea("intercept"), rt.NeedsManager(managers.Default))
}

const (
	agenthardTapHeader  = "X-Agenthard-Tap"
	agenthardTapValue   = "yes"
	agenthardTapTimeout = 60 * time.Second
	agenthardHugeLength = "4611686018427387904"
)

// Test_HugeContentLengthIsTapped sends a request that matches the filter of an HTTP wiretap,
// declares a Content-Length of 2^62 and never sends a body. The local service must receive
// the copy of that request within a bound, and the cluster must keep answering ordinary
// requests.
func (s *AgenthardWiretap) Test_HugeContentLengthIsTapped() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("agenthard-wiretap"))
	ls := s.LocalEcho()

	a := conn.Wiretap(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.HTTPHeader(agenthardTapHeader, agenthardTapValue))
	defer a.Detach(t)
	t.Cleanup(func() {
		// A stuck request handler is removed with the pod that runs it.
		_, _ = s.R().Kubectl(ctx, wl.Namespace, "delete", "pod", "-l", "app="+wl.Name, "--wait=false")
	})

	svcURL := wl.ServiceURL()
	rt.RoutedToClusterAndTapped(t, svcURL, ls, agenthardTapTimeout, check.WithHeader(agenthardTapHeader, agenthardTapValue))

	path := "/agenthard-" + fmt.Sprint(time.Now().UnixNano())
	ls.ResetRequests()
	c := agenthardHugeRequest(t, svcURL, path)
	defer c.Close()

	want := "POST " + path
	s.Eventually(func() bool {
		return slices.Contains(ls.Requests(), want)
	}, agenthardTapTimeout, 250*time.Millisecond, "the wiretap should copy the request %q to the local service", want)

	rt.RoutedToCluster(t, svcURL)
}

// agenthardHugeRequest opens a connection to the service at rawURL and sends the header of a
// POST request for path that declares a huge Content-Length and carries no body. The caller
// closes the returned connection.
func agenthardHugeRequest(t testing.TB, rawURL, path string) net.Conn {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %q: %v", rawURL, err)
	}
	c, err := net.DialTimeout("tcp", u.Host, 10*time.Second)
	if err != nil {
		t.Fatalf("dialing %s: %v", u.Host, err)
	}
	req := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: %s\r\n%s: %s\r\nContent-Type: application/octet-stream\r\nContent-Length: %s\r\n\r\n",
		path, u.Host, agenthardTapHeader, agenthardTapValue, agenthardHugeLength)
	if _, err := c.Write([]byte(req)); err != nil {
		_ = c.Close()
		t.Fatalf("writing request: %v", err)
	}
	return c
}
