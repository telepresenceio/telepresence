package dnet_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/net/nettest"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

var mockServerBinary string

func TestMain(m *testing.M) {
	mbf, err := os.CreateTemp("", "mockServer")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mockServerBinary = mbf.Name()
	if runtime.GOOS == "windows" {
		mockServerBinary += ".exe"
	}
	mbf.Close()
	ctx := dlog.WithLogger(context.Background(), dlog.WrapLogrus(logrus.StandardLogger()))
	cmd := dexec.CommandContext(ctx, "go", "build", "-o", mockServerBinary, ".")
	cmd.Dir = filepath.Join("testdata", "mockserver")
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.Remove(mockServerBinary)
	m.Run()
}

func TestKubectlPortForward(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.SkipNow()
	}
	if _, err := dexec.LookPath("socat"); err != nil {
		if runtime.GOOS == "linux" && os.Getenv("CI") != "" {
			t.Fatal("would skip this test in CI, which isn't OK")
		}
		t.SkipNow()
	}
	strPtr := func(s string) *string {
		return &s
	}

	makePipe := func() (_, _ net.Conn, _ func(), _err error) {
		ctx, cancel := context.WithCancel(dcontext.WithSoftness(dlog.NewTestContext(t, true)))
		grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
		var cliConn, srvConn net.Conn
		stop := func() {
			cancel()
			if err := grp.Wait(); err != nil {
				t.Error(err)
			}
			// This is 10% just to do cleanup, and is 90% to prevent the GC from calling
			// srvConn's finalizaer and closing the connection while the test is still
			// running.
			if cliConn != nil {
				_ = cliConn.Close()
			}
			if srvConn != nil {
				_ = srvConn.Close()
			}
		}
		defer func() {
			if _err != nil {
				stop()
			}
		}()

		podListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, nil, err
		}
		defer func() {
			if _err != nil {
				_ = podListener.Close()
			}
		}()

		apiserverListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, nil, err
		}
		apiserverAddr := apiserverListener.Addr().(*net.TCPAddr)
		_ = apiserverListener.Close()

		srvConnCh := make(chan net.Conn)
		apiReady := make(chan struct{})
		grp.Go("pod", func(_ context.Context) error {
			conn, err := podListener.Accept()
			t.Log("accepted")
			_ = podListener.Close()
			if err != nil {
				return err
			}
			srvConnCh <- conn
			return nil
		})
		grp.Go("apiserver", func(ctx context.Context) error {
			cmd := dexec.CommandContext(
				ctx, mockServerBinary, "-p", strconv.Itoa(apiserverAddr.Port))
			cmd.DisableLogging = true
			cmd.Stdout = dlog.StdLogger(ctx, dlog.LogLevelInfo).Writer()
			cmd.Stderr = dlog.StdLogger(ctx, dlog.LogLevelError).Writer()
			err := cmd.Start()
			if err != nil {
				close(apiReady)
				return err
			}
			for i := 0; i < 100; i++ {
				dtime.SleepWithContext(ctx, 10*time.Millisecond)
				var rsp *http.Response
				if rsp, err = http.DefaultClient.Get(fmt.Sprintf("http://localhost:%d/api", apiserverAddr.Port)); err == nil {
					rsp.Body.Close()
					close(apiReady)
					_ = cmd.Wait()
					return nil
				}
			}
			close(apiReady)
			return err
		})
		<-apiReady

		kubeFlags := &genericclioptions.ConfigFlags{
			KubeConfig: strPtr("/dev/null"),
			APIServer:  strPtr(fmt.Sprintf("http://localhost:%d", apiserverAddr.Port)),
		}
		kubeConfig, err := kubeFlags.ToRESTConfig()
		if err != nil {
			return nil, nil, nil, err
		}
		ki, err := kubernetes.NewForConfig(kubeConfig)
		if err != nil {
			return nil, nil, nil, err
		}
		dialer, err := dnet.NewK8sPortForwardDialer(ctx, kubeConfig, ki)
		if err != nil {
			return nil, nil, nil, err
		}

		cliConn, err = dialer.Dial(ctx, fmt.Sprintf("pods/SOMEPODNAME.SOMENAMESPACE:%d", podListener.Addr().(*net.TCPAddr).Port))
		t.Log("dialed")
		if err != nil {
			return nil, nil, nil, err
		}

		srvConn = <-srvConnCh
		return cliConn, srvConn, stop, nil
	}
	// Can't test Client side using nettest.TestConn, because the net.Conn exposed by the spdystream.Stream doesn't return the
	// expected net.Error (it returns io.EOF).
	// t.Run("Client", func(t *testing.T) { nettest.TestConn(t, makePipe) })
	t.Run("Server", func(t *testing.T) { nettest.TestConn(t, flipMakePipe(makePipe)) })
}
