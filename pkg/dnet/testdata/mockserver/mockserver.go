package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec" //nolint:depguard // We really do want the socat to be minimal
	"path"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/kubelet/cri/streaming/portforward"

	"github.com/datawire/dlib/dhttp"
)

func main() {
	var port uint
	flag.UintVar(&port, "p", 0, "Port to listen to")
	flag.Parse()
	if err := runMockAPIServer(context.Background(), port); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

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

func (s *mockAPIServer) PortForward(_ context.Context, _ string, _ types.UID, port int32, stream io.ReadWriteCloser) error {
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
	fmt.Printf("ACCESS '%s' '%s'\n", r.Method, urlpath)

	if urlpath == "/api" {
		data := map[string]any{
			"kind": "APIVersions",
			"versions": []string{
				"v1",
			},
			"serverAddressByClientCIDRs": []map[string]any{
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
		data := map[string]any{
			"kind":         "APIResourceList",
			"groupVersion": "v1",
			"resources": []map[string]any{
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
		data := map[string]any{
			"kind":       "Pod",
			"apiVersion": "v1",
			"metadata": map[string]any{
				"name":      match[2],
				"namespace": match[1],
			},
			"spec": map[string]any{
				"containers": []map[string]any{
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

func runMockAPIServer(ctx context.Context, port uint) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
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
