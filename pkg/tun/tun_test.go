package tun

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/signal"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"golang.org/x/sys/unix"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/socks"
)

func TestTun(t *testing.T) {
	dtest.Sudo()
	dtest.WithMachineLock(func() {
		suite.Run(t, new(tunSuite))
	})
}

type tunSuite struct {
	suite.Suite
	ctx        context.Context
	dispatcher *Dispatcher
}

func (ts *tunSuite) NewDialer(ctx context.Context, _ string, proxyPort uint16) (socks.Dialer, error) {
	return ts.fakeSocksDialer(ctx, proxyPort), nil
}

func (ts *tunSuite) SetupSuite() {
	t := ts.T()
	ctx := dlog.WithLogger(context.Background(), dlog.WrapTB(t, false))
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	t.Cleanup(cancel)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, unix.SIGINT, unix.SIGTERM, unix.SIGQUIT, unix.SIGABRT, unix.SIGHUP)
	go func() {
		<-sigCh
		cancel()
	}()
	ts.ctx = ctx

	require := ts.Require()
	tun, err := OpenTun()
	require.NoError(err, "Failed to open TUN device")

	socks.Proxy = ts
	ts.dispatcher = NewDispatcher(tun)
	require.NoError(ts.dispatcher.SetProxyPort(ctx, 1080))
	dlog.Debugf(ctx, "setup complete")
}

type fakeDialer struct {
	addr      string
	proxyPort uint16
}

func (f *fakeDialer) ProxyPort() uint16 {
	return f.proxyPort
}

func (f *fakeDialer) DialContext(c context.Context, src net.IP, srcPort uint16, dest net.IP, destPort uint16) (net.Conn, error) {
	dlog.Debugf(c, "dialing %s %s.%d -> %s.%d", "tcp", src, srcPort, dest, destPort)
	dialer := net.Dialer{}
	return dialer.DialContext(c, "tcp", f.addr)
}

// fakeSocksDialer creates a local service that just listens to "/" and echoes the host. A
// dialer for the service is returned
func (ts *tunSuite) fakeSocksDialer(c context.Context, proxyPort uint16) *fakeDialer {
	mux := http.NewServeMux()
	c = dgroup.WithGoroutineName(c, "socks service")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		dlog.Debugf(c, "http serving %s", r.Host)
		fmt.Fprintln(w, r.Host)
	})

	lc := net.ListenConfig{}
	l, err := lc.Listen(c, "tcp", "127.0.0.1:0")
	ts.Require().NoError(err)

	go func() {
		srv := &dhttp.ServerConfig{Handler: mux}
		_ = srv.Serve(c, l)
	}()
	return &fakeDialer{addr: l.Addr().String(), proxyPort: proxyPort}
}

func (ts *tunSuite) TestTunnel() {
	require := ts.Require()
	addr, err := subnet.FindAvailableClassC()
	require.NoError(err)

	to := make(net.IP, 4)
	copy(to, addr.IP)
	to[3] = 1

	testIP := make(net.IP, 4)
	copy(testIP, addr.IP)
	testIP[3] = 123

	require.NoError(ts.dispatcher.dev.AddSubnet(ts.ctx, addr, to))

	go func() {
		ts.NoError(ts.dispatcher.Run(ts.ctx))
	}()

	dlog.Debugf(ts.ctx, "http://%s:8080/", testIP)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8080/", testIP))
	require.NoError(err)
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	require.NoError(err)
	ts.dispatcher.Stop(ts.ctx)
	dlog.Info(ts.ctx, string(data))
}

func (ts *tunSuite) TearDownSuite() {
}
