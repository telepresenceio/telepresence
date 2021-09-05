package dnet

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/util"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
)

type k8sPortForwardDialer struct {
	// static
	logCtx          context.Context
	kubeFlags       *kates.ConfigFlags
	kubeRESTClient  *rest.RESTClient
	kubeKatesClient *kates.Client
	spdyTransport   http.RoundTripper
	spdyUpgrader    spdy.Upgrader

	// state
	nextRequestID int64
	spdyStreamsMu sync.Mutex
	spdyStreams   map[string]httpstream.Connection // key is "podname.namespace"
}

// NewK8sPortForwardDialer returns a dialer function (matching the signature required by
// grpc.WithContextDialer) that dials to a port on a Kubernetes Pod, in the manor of `kubectl
// port-forward`.  It returns the direct connection to the apiserver; it does not establish a local
// port being forwarded from or otherwise pump data over the connection.
func NewK8sPortForwardDialer(logCtx context.Context, kubeFlags *kates.ConfigFlags, kubeKatesClient *kates.Client) (func(context.Context, string) (net.Conn, error), error) {
	kubeConfig, err := kubeFlags.ToRESTConfig()
	if err != nil {
		return nil, err
	}

	if err := setKubernetesDefaults(kubeConfig); err != nil {
		return nil, err
	}

	kubeRESTClient, err := rest.RESTClientFor(kubeConfig)
	if err != nil {
		return nil, err
	}

	spdyTransport, spdyUpgrader, err := spdy.RoundTripperFor(kubeConfig)
	if err != nil {
		return nil, err
	}
	dialer := &k8sPortForwardDialer{
		logCtx:          logCtx,
		kubeFlags:       kubeFlags,
		kubeRESTClient:  kubeRESTClient,
		kubeKatesClient: kubeKatesClient,
		spdyTransport:   spdyTransport,
		spdyUpgrader:    spdyUpgrader,

		spdyStreams: make(map[string]httpstream.Connection),
	}
	return dialer.Dial, nil
}

// Dial dials a port of something in the cluster.  The address format is
// "[objkind/]objname[.objnamespace]:port".
func (pf *k8sPortForwardDialer) Dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	dlog.Debugf(pf.logCtx, "k8sPortForwardDialer.Dial(ctx, %q)", addr)

	pod, podPortNumber, err := pf.resolve(ctx, addr)
	if err != nil {
		return nil, err
	}
	conn, err = pf.dial(ctx, pod, podPortNumber)
	if err != nil {
		dlog.Errorf(ctx, "Error with k8sPortForwardDialer dial: %s", err)
	}
	return conn, err
}

func (pf *k8sPortForwardDialer) resolve(ctx context.Context, addr string) (*kates.Pod, uint16, error) {
	hostName, portName, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, 0, err
	}

	// 1. Get the object.
	var objKind, objQName string
	if slash := strings.Index(hostName, "/"); slash < 0 {
		objKind = "Pod."
		objQName = hostName
	} else {
		objKind = hostName[:slash]
		objQName = hostName[slash+1:]
	}
	var objName, objNamespace string
	if dot := strings.LastIndex(objQName, "."); dot < 0 {
		objName = objQName
		objNamespace = ""
	} else {
		objName = objQName[:dot]
		objNamespace = objQName[dot+1:]
	}
	objUn := &kates.Unstructured{
		Object: map[string]interface{}{
			"kind": objKind,
			"metadata": map[string]interface{}{
				"name":      objName,
				"namespace": objNamespace,
			},
		},
	}
	dlog.Debugf(pf.logCtx, "kates.Get(%s %s.%s)", objKind, objName, objNamespace)
	if err := pf.kubeKatesClient.Get(ctx, objUn, objUn); err != nil {
		return nil, 0, fmt.Errorf("unable to get %s %s.%s: %w", objKind, objName, objNamespace, err)
	}
	obj, err := kates.NewObjectFromUnstructured(objUn)
	if err != nil {
		return nil, 0, err
	}

	// 2. Resolve that object to a Pod (it might be something else that refers to a Pod, such as
	// a Service).
	pod, err := polymorphichelpers.AttachablePodForObjectFn(pf.kubeFlags, obj, func() time.Duration {
		// This is the same timeout that the `kubectl port-forward` `--pod-running-timeout`
		// flag sets.
		if deadline, ok := ctx.Deadline(); ok {
			return time.Until(deadline)
		}
		// Fall back to the same default as --pod-running-timeout.
		return time.Minute
	}())
	if err != nil {
		return nil, 0, err
	}

	// 3. Resolve the port number.
	var podPortNumber uint16
	switch obj := obj.(type) {
	case *corev1.Service:
		svc := obj
		svcPortNumber, err := func() (int32, error) {
			if svcPortNumber, err := strconv.Atoi(portName); err == nil {
				return int32(svcPortNumber), nil
			}
			return util.LookupServicePortNumberByName(*svc, portName)
		}()
		if err != nil {
			return nil, 0, err
		}
		containerPortNumber, err := util.LookupContainerPortNumberByServicePort(*svc, *pod, svcPortNumber)
		if err != nil {
			return nil, 0, err
		}
		podPortNumber = uint16(containerPortNumber)
	default:
		containerPortNumber, err := func() (int32, error) {
			if containerPortNumber, err := strconv.Atoi(portName); err == nil {
				return int32(containerPortNumber), nil
			}
			return util.LookupContainerPortNumberByName(*pod, portName)
		}()
		if err != nil {
			return nil, 0, err
		}
		podPortNumber = uint16(containerPortNumber)
	}

	return pod, podPortNumber, nil
}

func (pf *k8sPortForwardDialer) spdyStream(pod *kates.Pod) (httpstream.Connection, error) {
	cacheKey := pod.Name + "." + pod.Namespace
	pf.spdyStreamsMu.Lock()
	defer pf.spdyStreamsMu.Unlock()
	if spdyStream, ok := pf.spdyStreams[cacheKey]; ok {
		return spdyStream, nil
	}

	// Most of the Kubernetes API is HTTP/2+gRPC, not SPDY; and so that's what client-go mostly
	// helps us with.  So in order to get the URL to use in the SPDY request, we're going to
	// build a standard Kubernetes HTTP/2 *rest.Request and extract the URL from that, and
	// discard the rest of the *rest.Request.
	reqURL := pf.kubeRESTClient.
		Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("portforward").
		URL()

	// Don't bother caching dialers in .pf, they're just statelss utility structures.
	spdyDialer := spdy.NewDialer(pf.spdyUpgrader, &http.Client{Transport: pf.spdyTransport}, http.MethodPost, reqURL)

	dlog.Debugf(pf.logCtx, "k8sPortForwardDialer.spdyDial(ctx, Pod./%s.%s)", pod.Name, pod.Namespace)

	spdyStream, _, err := spdyDialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, err
	}

	pf.spdyStreams[cacheKey] = spdyStream
	go func() {
		<-spdyStream.CloseChan()
		pf.spdyStreamsMu.Lock()
		delete(pf.spdyStreams, cacheKey)
		pf.spdyStreamsMu.Unlock()
	}()

	return spdyStream, nil
}

func (pf *k8sPortForwardDialer) dial(ctx context.Context, pod *kates.Pod, port uint16) (conn *kpfConn, err error) {
	dlog.Debugf(pf.logCtx, "k8sPortForwardDialer.dial(ctx, %s.%s, %d)",
		pod.Name,
		pod.Namespace,
		port)

	// All port-forwards to the same Pod get multiplexed over the same SPDY stream.
	spdyStream, err := pf.spdyStream(pod)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			pf.spdyStreamsMu.Lock()
			delete(pf.spdyStreams, pod.Name+"."+pod.Namespace)
			pf.spdyStreamsMu.Unlock()
		}
	}()

	requestID := atomic.AddInt64(&pf.nextRequestID, 1) - 1

	headers := http.Header{}
	headers.Set(corev1.PortHeader, strconv.FormatInt(int64(port), 10))
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.FormatInt(requestID, 10))

	// Quick note: spdyStream.CreateStream returns httpstream.Stream objects.  These have
	// confusing method names compared to net.Conn objects:
	//
	//   |                            | net.Conn     | httpstream.Stream |
	//   |----------------------------+--------------+-------------------|
	//   | close both ends            | Close()      | Reset()           |
	//   | close just the 'read' end  | CloseRead()  | -                 |
	//   | close just the 'write' end | CloseWrite() | Close()           |

	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	errorStream, err := spdyStream.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("create port-forward error stream: %w", err)
	}
	// errorStream is read-only, we can go ahead and close the 'write' end.
	_ = errorStream.Close()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := spdyStream.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("create port-forward data stream: %w", err)
	}

	conn = &kpfConn{
		remoteAddr: net.JoinHostPort(pod.Name+"."+pod.Namespace, strconv.FormatInt(int64(port), 10)),

		errorStream: errorStream,
		dataStream:  dataStream,

		oobErrCh: make(chan struct{}),

		readDeadline:  makePipeDeadline(),
		writeDeadline: makePipeDeadline(),
	}
	go conn.oobWorker()
	return conn, nil
}

type kpfConn struct {
	remoteAddr string

	errorStream httpstream.Stream
	dataStream  httpstream.Stream

	oobErrCh chan struct{}
	oobErr   error

	readMu       sync.Mutex
	readDeadline pipeDeadline
	readErr      error

	writeMu       sync.Mutex
	writeDeadline pipeDeadline
	writeErr      error
}

func (c *kpfConn) oobWorker() {
	msg, err := io.ReadAll(c.errorStream)
	switch {
	case err != nil:
		c.oobErr = fmt.Errorf("reading error stream: %w", err)
	case len(msg) > 0:
		c.oobErr = fmt.Errorf("error stream: %s", msg)
	}
	close(c.oobErrCh)
}

// Read implements net.Conn.
func (c *kpfConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if c.readErr != nil {
		return 0, c.readErr
	}
	switch {
	case isClosedChan(c.oobErrCh) && c.oobErr != nil:
		return 0, c.oobErr
	case isClosedChan(c.readDeadline.wait()):
		return 0, os.ErrDeadlineExceeded
	}

	n, err := c.dataStream.Read(b)
	if err != nil {
		c.readErr = err
	}
	return n, err
}

// Write implements net.Conn.
func (c *kpfConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.writeErr != nil {
		return 0, c.writeErr
	}
	switch {
	case isClosedChan(c.oobErrCh) && c.oobErr != nil:
		return 0, c.oobErr
	case isClosedChan(c.writeDeadline.wait()):
		return 0, os.ErrDeadlineExceeded
	}

	n, err := c.dataStream.Write(b)
	if err != nil {
		c.writeErr = err
	}
	return n, err
}

// Close implements net.Conn.
func (c *kpfConn) Close() error {
	closeErr := c.dataStream.Reset()
	<-c.oobErrCh
	if c.oobErr != nil {
		return c.oobErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// CloseWrite augments net.Conn.
func (c *kpfConn) CloseWrite() error {
	closeErr := c.dataStream.Close()
	if isClosedChan(c.oobErrCh) && c.oobErr != nil {
		return c.oobErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// LocalAddr implements net.Conn.
func (c *kpfConn) LocalAddr() net.Addr {
	return addr{
		net:  "kubectl-port-forward",
		addr: "client",
	}
}

// RemoteAddr implements net.Conn.
func (c *kpfConn) RemoteAddr() net.Addr {
	return addr{
		net:  "kubectl-port-forward",
		addr: c.remoteAddr,
	}
}

// SetDeadline implements net.Conn.
func (c *kpfConn) SetDeadline(t time.Time) error {
	c.readDeadline.set(t)
	c.writeDeadline.set(t)
	return nil
}

// SetReadDeadline implements net.Conn.
func (c *kpfConn) SetReadDeadline(t time.Time) error {
	c.readDeadline.set(t)
	return nil
}

// SetWriteDeadline implements net.Conn.
func (c *kpfConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline.set(t)
	return nil
}
