package dnet_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	//nolint:depguard // We really do want the socat to be minimal
	"os/exec"

	"golang.org/x/net/nettest"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/kubelet/cri/streaming/portforward"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

type featurefulResponseWriter interface {
	http.ResponseWriter
	http.Hijacker
}

type callbackResponseHijacker struct {
	featurefulResponseWriter
	cb func(net.Conn)
}

func (h *callbackResponseHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, e := h.featurefulResponseWriter.Hijack()
	if c != nil {
		h.cb(c)
	}
	return c, b, e
}

type mockAPIServer struct {
	onShutdown chan struct{}
}

func (s *mockAPIServer) PortForward(_ string, _ types.UID, port int32, stream io.ReadWriteCloser) error {
	if port <= 0 || port > math.MaxUint16 {
		return fmt.Errorf("invalid port %d", port)
	}

	// This mimics kubernetes.git kubernetes/pkg/kubelet/dockershim/docker_streaming_other.go

	cmd := exec.Command("socat", "STDIO", fmt.Sprintf("TCP4:localhost:%d", port))

	// stdout
	cmd.Stdout = stream

	// stderr
	stderr := new(strings.Builder)
	cmd.Stderr = stderr

	// stdin
	inPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("unable to do port forwarding: error creating stdin pipe: %w", err)
	}
	go func() {
		_, _ = io.Copy(inPipe, stream)
		inPipe.Close()
	}()

	// run
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func (s *mockAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	urlpath := path.Clean(r.URL.Path)
	dlog.Infof(r.Context(), "ACCESS '%s' '%s'", r.Method, urlpath)

	if urlpath == "/api" {
		data := map[string]interface{}{
			"kind": "APIVersions",
			"versions": []string{
				"v1",
			},
			"serverAddressByClientCIDRs": []map[string]interface{}{
				{
					"clientCIDR":    "0.0.0.0/0",
					"serverAddress": "10.88.3.3:6443",
				},
			},
		}
		bs, _ := json.Marshal(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bs)
	} else if urlpath == "/api/v1" {
		data := map[string]interface{}{
			"kind":         "APIResourceList",
			"groupVersion": "v1",
			"resources": []map[string]interface{}{
				{
					"name":       "pods",
					"namespaced": true,
					"kind":       "Pod",
					"verbs":      []string{"get"},
				},
				{
					"name":       "pods/portforward",
					"namespaced": true,
					"kind":       "PodPortForwardOptions",
					"verbs": []string{
						"create",
						"get",
					},
				},
			},
		}
		bs, _ := json.Marshal(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bs)
	} else if match := regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/pods/([^/]+)$`).FindStringSubmatch(urlpath); match != nil {
		// "/api/v1/namespaces/{namespace}/pods/{podname}"
		data := map[string]interface{}{
			"kind":       "Pod",
			"apiVersion": "v1",
			"metadata": map[string]interface{}{
				"name":      match[2],
				"namespace": match[1],
			},
			"spec": map[string]interface{}{
				"containers": []map[string]interface{}{
					{
						"name": "some-container",
					},
				},
			},
		}
		bs, _ := json.Marshal(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bs)
	} else if match := regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/pods/([^/]+)/portforward$`).FindStringSubmatch(urlpath); match != nil {
		// "/api/v1/namespaces/{namespace}/pods/{podname}/portforward"

		// The SPDY implementation does not give us a way to tell it to shut down, so we'll
		// forcefully .Close() the connection if <-s.onShutdown.
		connCh := make(chan net.Conn)
		w = &callbackResponseHijacker{
			featurefulResponseWriter: w.(featurefulResponseWriter),
			cb: func(conn net.Conn) {
				connCh <- conn
			},
		}
		doneCh := make(chan struct{})
		go func() {
			defer close(doneCh)
			portforward.ServePortForward(w, r,
				s, // PortForwarder
				"bogus-pod-name",
				"bogus-pod-uid",
				nil,           // *portforward.V4Options; only used for WebSockets-based proto, but we only support SPDY-base proto
				1*time.Minute, // idleTimeout
				1*time.Minute, // streamCreationTimeout
				portforward.SupportedProtocols)
		}()
		select {
		case conn := <-connCh:
			select {
			case <-s.onShutdown:
				conn.Close()
				// We "should" wait here, but in some cases the SDPY implementation
				// is even more misbehaved than usual.
				//
				// <-doneCh
			case <-doneCh:
			}
		case <-doneCh:
		}
	} else {
		http.NotFound(w, r)
	}
}

func RunMockAPIServer(ctx context.Context, listener net.Listener) error {
	onShutdown := make(chan struct{})
	sc := dhttp.ServerConfig{
		Handler: &mockAPIServer{
			onShutdown: onShutdown,
		},
		OnShutdown: []func(){
			func() { close(onShutdown) },
		},
	}
	return sc.Serve(ctx, listener)
}

func TestKubectlPortForward(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.SkipNow()
	}
	if _, err := exec.LookPath("socat"); err != nil {
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
		defer func() {
			if _err != nil {
				_ = apiserverListener.Close()
			}
		}()

		kubeFlags := &genericclioptions.ConfigFlags{
			KubeConfig: strPtr("/dev/null"),
			APIServer:  strPtr(fmt.Sprintf("http://localhost:%d", apiserverListener.Addr().(*net.TCPAddr).Port)),
		}
		kubeConfig, err := kubeFlags.ToRESTConfig()
		if err != nil {
			return nil, nil, nil, err
		}
		ki, err := kubernetes.NewForConfig(kubeConfig)
		if err != nil {
			return nil, nil, nil, err
		}
		dial, err := dnet.NewK8sPortForwardDialer(ctx, kubeConfig, ki)
		if err != nil {
			return nil, nil, nil, err
		}

		srvConnCh := make(chan net.Conn)
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
			return RunMockAPIServer(ctx, apiserverListener)
		})

		cliConn, err = dial(ctx, fmt.Sprintf("pods/SOMEPODNAME.SOMENAMESPACE:%d", podListener.Addr().(*net.TCPAddr).Port))
		t.Log("dialed")
		if err != nil {
			return nil, nil, nil, err
		}

		srvConn = <-srvConnCh
		return cliConn, srvConn, stop, nil
	}
	t.Run("Client", func(t *testing.T) { nettest.TestConn(t, makePipe) })
	t.Run("Server", func(t *testing.T) { nettest.TestConn(t, flipMakePipe(makePipe)) })
}
