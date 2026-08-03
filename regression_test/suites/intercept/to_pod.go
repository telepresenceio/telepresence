package intercept

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// toPodTimeout bounds the wait for a --to-pod forwarded sidecar port to
// answer.
const toPodTimeout = 15 * time.Second

// toPodDialTimeout bounds the single negative probe against the sidecar
// port the intercept never requested a forward for: it must fail quickly,
// not eventually.
const toPodDialTimeout = 2 * time.Second

// anyHTTPResponse is satisfied by any response at all: proving a --to-pod
// forward works only requires that something answered, not any particular
// status or body (the sidecars are plain echo-server containers).
func anyHTTPResponse(int, string) bool { return true }

// ToPod proves that --to-pod forwards the requested pod ports to the
// workstation's localhost while an intercept is active, each at the same
// port number as in the pod (pkg/client/cli/intercept/command.go's --to-pod
// flag; pkg/client/userd/trafficmgr/podaccess.go's workerPortForward parses
// each requested port and starts a forwarder.New listening on that same
// port locally, targeting the pod IP on it), and that a pod port never named
// in --to-pod is not forwarded.
type ToPod struct {
	rt.Suite
}

func init() {
	rt.Register(&ToPod{}, rt.InArea("intercept"), rt.NeedsManager(managers.Default))
}

// Test_ToPodForwardsRequestedPorts intercepts a workload with three sidecar
// echo containers on distinct free local ports, requests --to-pod forwards
// for the first two, and asserts those two eventually answer over HTTP on
// localhost while the third, unrequested one never does.
func (s *ToPod) Test_ToPodForwardsRequestedPorts() {
	t := s.T()
	conn := s.Connect()

	// The local side of a --to-pod forward binds the pod's own port number
	// on the workstation, so the ports picked here must be free locally.
	ports, err := ioutil.FreePortsTCP(3)
	s.Require().NoError(err, "allocating free local ports for the to-pod sidecars")

	tpl := workloads.Echo("to-pod")
	tpl.ExtraContainers = []workloads.ExtraContainer{
		{Name: "sidecar-a", Port: int32(ports[0].Port())},
		{Name: "sidecar-b", Port: int32(ports[1].Port())},
		{Name: "sidecar-c", Port: int32(ports[2].Port())},
	}
	wl := s.Workload(tpl)
	ls := s.LocalEcho()

	forwarded := tpl.ExtraContainers[:2]
	notForwarded := tpl.ExtraContainers[2]

	opts := []cli.InterceptOpt{rt.ToLocal(ls, "http"), cli.MountFalse()}
	for _, c := range forwarded {
		opts = append(opts, cli.ToPod(strconv.Itoa(int(c.Port))))
	}
	a := conn.Intercept(t, wl, opts...)
	defer a.Detach(t)

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	for _, c := range forwarded {
		url := fmt.Sprintf("http://localhost:%d", c.Port)
		check.EventuallyHTTP(t, url, anyHTTPResponse, toPodTimeout)
	}

	url := fmt.Sprintf("http://localhost:%d", notForwarded.Port)
	ctx, cancel := context.WithTimeout(s.Ctx(), toPodDialTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	s.Require().NoError(err)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("localhost:%d answered, but --to-pod never requested a forward for it", notForwarded.Port)
	}
}
