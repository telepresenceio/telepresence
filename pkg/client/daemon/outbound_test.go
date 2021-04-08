package daemon

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
)

type outboundSuite struct {
	suite.Suite
	o      *outbound
	ctx    context.Context
	cancel context.CancelFunc
	g      *dgroup.Group
}

func TestOutbound(t *testing.T) {
	dtest.Sudo()
	dtest.WithMachineLock(func() {
		suite.Run(t, new(outboundSuite))
	})
}

func (s *outboundSuite) SetupSuite() {
	require := s.Require()
	s.ctx, s.cancel = context.WithCancel(dlog.NewTestContext(s.T(), false))
	var err error
	s.o, err = newOutbound(s.ctx, "", false)
	require.NoError(err)

	// What normally would be a proxy is replaced with a http server that just
	// echoes the host
	port, err := httpService(s.ctx)
	require.NoError(err)
	s.o.proxyRedirPort = port

	s.g = dgroup.NewGroup(s.ctx, dgroup.GroupConfig{})
	s.g.Go("server-dns", func(ctx context.Context) error {
		return s.o.dnsServerWorker(ctx)
	})
	s.g.Go("firewall-configurator", func(ctx context.Context) error {
		return s.o.routerConfigurationWorker(ctx)
	})
}

// httpService creates a local service that just listens to "/" and echoes the host. The
// allocated port is returned.
func httpService(c context.Context) (int, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, r.Host)
	})

	lc := net.ListenConfig{}
	l, err := lc.Listen(c, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return 0, err
	}

	go func() {
		srv := &dhttp.ServerConfig{Handler: mux}
		_ = srv.Serve(c, l)
	}()
	return strconv.Atoi(portStr)
}

func (s *outboundSuite) TearDownSuite() {
	s.cancel()
	s.o.noMoreUpdates()
	s.NoError(s.g.Wait())
}

// Use TEST-NET-[123] so that we know what to delete if something goes sideways.
var networks = []net.IP{
	{192, 0, 2, 0},
	{198, 51, 100, 0},
	{203, 0, 113, 0},
}

// TestMassiveUpdate routes port 80 and 8080 to all IP's in the given networks to the
// "proxy" (the echoing http-server) and validates that all destinations can be reached.
func (s *outboundSuite) TestMassiveUpdate() {
	require := s.Require()

	// Fill all three networks with 255 routes (last byte 1 - 255)
	routes := make(map[IPKey]struct{}, 255*3)
	ri := 0
	var err error
	for _, network := range networks {
		for i := 1; i < 256; i++ {
			ip := make(net.IP, 4)
			copy(ip, network)
			ip[3] = byte(i)
			routes[IPKey(ip)] = struct{}{}
			require.NoError(err)
			ri++
		}
	}

	start := time.Now()
	err = s.o.doUpdate(s.ctx, nil, routes)
	require.NoError(err)
	timeSpent := time.Since(start)
	s.Truef(5*time.Second > timeSpent, "Update of routes took %s", timeSpent)
	s.T().Logf("Update of %d routes took %s", len(routes)*2, timeSpent)

	client := http.Client{Timeout: 200 * time.Millisecond}
	for _, network := range networks {
		for i := 1; i < 256; i++ {
			for _, port := range []int{80, 8080} {
				ip := make(net.IP, 4)
				copy(ip, network)
				ip[3] = byte(i)
				host := fmt.Sprintf("%s:%d", ip, port)
				r, err := client.Get("http://" + host)
				require.NoError(err)
				b := r.Body
				txt, err := ioutil.ReadAll(b)
				b.Close()
				require.NoError(err)
				require.Equal(host, strings.TrimSpace(string(txt)))
			}
		}
	}
}
