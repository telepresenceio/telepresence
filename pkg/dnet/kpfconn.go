package dnet

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/util"
	"k8s.io/kubectl/pkg/util/podutils"

	"github.com/datawire/dlib/dlog"
)

type k8sPortForwardDialer struct {
	// static
	logCtx        context.Context
	k8sInterface  kubernetes.Interface
	spdyTransport http.RoundTripper
	spdyUpgrader  spdy.Upgrader

	// state
	nextRequestID int64
	spdyStreamsMu sync.Mutex
	spdyStreams   map[string]httpstream.Connection // key is "podname.namespace"
}

// NewK8sPortForwardDialer returns a dialer function (matching the signature required by
// grpc.WithContextDialer) that dials to a port on a Kubernetes Pod, in the manor of `kubectl
// port-forward`.  It returns the direct connection to the apiserver; it does not establish a local
// port being forwarded from or otherwise pump data over the connection.
func NewK8sPortForwardDialer(logCtx context.Context, kubeConfig *rest.Config, k8sInterface kubernetes.Interface) (func(context.Context, string) (net.Conn, error), error) {
	if err := setKubernetesDefaults(kubeConfig); err != nil {
		return nil, err
	}
	spdyTransport, spdyUpgrader, err := spdy.RoundTripperFor(kubeConfig)
	if err != nil {
		return nil, err
	}
	dialer := &k8sPortForwardDialer{
		logCtx:        logCtx,
		k8sInterface:  k8sInterface,
		spdyTransport: spdyTransport,
		spdyUpgrader:  spdyUpgrader,

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
	inner, err := pf.dial(ctx, pod, podPortNumber)
	if err != nil {
		dlog.Errorf(pf.logCtx, "Error with k8sPortForwardDialer dial: %s", err)
		return nil, err
	}
	return wrapUnbufferedConn(inner), nil
}

func (pf *k8sPortForwardDialer) resolve(ctx context.Context, addr string) (pod *core.Pod, podPortNumber uint16, err error) {
	var hostName, portName string
	hostName, portName, err = net.SplitHostPort(addr)
	if err != nil {
		return nil, 0, err
	}

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

	coreV1 := pf.k8sInterface.CoreV1()
	switch objKind {
	case "svc":
		// Get the service.
		var svc *core.Service
		if svc, err = coreV1.Services(objNamespace).Get(ctx, objName, meta.GetOptions{}); err != nil {
			return nil, 0, err
		}
		svcPortNumber, err := func() (int32, error) {
			if svcPortNumber, err := strconv.Atoi(portName); err == nil {
				return int32(svcPortNumber), nil
			}
			return util.LookupServicePortNumberByName(*svc, portName)
		}()
		if err != nil {
			return nil, 0, fmt.Errorf("cannot find service port in %s.%s: %v", objName, objNamespace, err)
		}

		// Resolve the Service to a Pod.
		var selector labels.Selector
		var podNS string
		podNS, selector, err = polymorphichelpers.SelectorsForObject(svc)
		if err != nil {
			return nil, 0, fmt.Errorf("cannot attach to %T: %v", svc, err)
		}
		timeout := func() time.Duration {
			if deadline, ok := ctx.Deadline(); ok {
				return time.Until(deadline)
			}
			// Fall back to the same default as --pod-running-timeout.
			return time.Minute
		}()

		sortBy := func(pods []*core.Pod) sort.Interface { return sort.Reverse(podutils.ActivePods(pods)) }
		pod, _, err = polymorphichelpers.GetFirstPod(coreV1, podNS, selector.String(), timeout, sortBy)
		if err != nil {
			return nil, 0, fmt.Errorf("cannot find first pod for %s.%s: %v", objName, objNamespace, err)
		}
		containerPortNumber, err := util.LookupContainerPortNumberByServicePort(*svc, *pod, svcPortNumber)
		if err != nil {
			return nil, 0, fmt.Errorf("cannot find first container port %s.%s: %v", pod.Name, pod.Namespace, err)
		}
		podPortNumber = uint16(containerPortNumber)
	default:
		// Get the pod.
		pod, err = coreV1.Pods(objNamespace).Get(ctx, objName, meta.GetOptions{})
		if err != nil {
			return nil, 0, fmt.Errorf("unable to get %s %s.%s: %w", objKind, objName, objNamespace, err)
		}
		var pn int32
		if p, err := strconv.Atoi(portName); err == nil {
			pn = int32(p)
		} else if pn, err = util.LookupContainerPortNumberByName(*pod, portName); err != nil {
			return nil, 0, err
		}
		podPortNumber = uint16(pn)
	}
	return pod, podPortNumber, nil
}

func (pf *k8sPortForwardDialer) spdyStream(pod *core.Pod) (httpstream.Connection, error) {
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
	reqURL := pf.k8sInterface.CoreV1().RESTClient().
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

func (pf *k8sPortForwardDialer) dial(ctx context.Context, pod *core.Pod, port uint16) (conn *kpfConn, err error) {
	dlog.Debugf(pf.logCtx, "k8sPortForwardDialer.dial(ctx, Pod./%s.%s, %d)",
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
	headers.Set(core.PortHeader, strconv.FormatInt(int64(port), 10))
	headers.Set(core.PortForwardRequestIDHeader, strconv.FormatInt(requestID, 10))

	// Quick note: spdyStream.CreateStream returns httpstream.Stream objects.  These have
	// confusing method names compared to net.Conn objects:
	//
	//   |                            | net.Conn     | httpstream.Stream |
	//   |----------------------------+--------------+-------------------|
	//   | close both ends            | Close()      | Reset()           |
	//   | close just the 'read' end  | CloseRead()  | -                 |
	//   | close just the 'write' end | CloseWrite() | Close()           |

	headers.Set(core.StreamType, core.StreamTypeError)
	errorStream, err := spdyStream.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("create port-forward error stream: %w", err)
	}
	// errorStream is read-only, we can go ahead and close the 'write' end.
	_ = errorStream.Close()

	headers.Set(core.StreamType, core.StreamTypeData)
	dataStream, err := spdyStream.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("create port-forward data stream: %w", err)
	}

	conn = &kpfConn{
		remoteAddr:  net.JoinHostPort(pod.Name+"."+pod.Namespace, strconv.FormatInt(int64(port), 10)),
		errorStream: errorStream,
		dataStream:  dataStream,
	}
	conn.init()
	return conn, nil
}

type kpfConn struct {
	// Configuration

	remoteAddr string
	// See the above comment about httpstream.Stream close semantics.
	errorStream httpstream.Stream
	dataStream  httpstream.Stream

	// Internal data

	oobErrCh chan struct{}
	oobErr   error // may only access .oobErr if .oobErrCh is closed (unless you're .oobWorker()).

	readBuff []byte
	readErr  error
	writeErr error
}

func (c *kpfConn) init() {
	c.oobErrCh = make(chan struct{})
	c.readBuff = make([]byte, c.MTU())
	go c.oobWorker()
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

// MTU implements unbufferedConn.
func (c *kpfConn) MTU() int {
	// 4MiB... I don't have a good reason why.  The gRPC-based unbufferedConns use an MTU of
	// 3MiB, but we have less overhead than them, so let's go a bit bigger?
	return 4 * 1024 * 1024
}

// Recv implements unbufferedConn.
func (c *kpfConn) Recv() ([]byte, error) {
	switch {
	case c.readErr != nil:
		return nil, c.readErr
	case isClosedChan(c.oobErrCh) && c.oobErr != nil:
		return nil, c.oobErr
	default:
		n, err := c.dataStream.Read(c.readBuff)
		if err != nil {
			c.readErr = err
		}
		return c.readBuff[:n], err
	}
}

// Send implements unbufferedConn.
func (c *kpfConn) Send(b []byte) error {
	switch {
	case c.writeErr != nil:
		return c.writeErr
	case isClosedChan(c.oobErrCh) && c.oobErr != nil:
		return c.oobErr
	default:
		_, err := c.dataStream.Write(b)
		if err != nil {
			c.writeErr = err
		}
		return err
	}
}

// CloseOnce implements unbufferedConn.
func (c *kpfConn) CloseOnce() error {
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

// LocalAddr implements unbufferedConn.
func (c *kpfConn) LocalAddr() net.Addr {
	return addr{
		net:  "kubectl-port-forward",
		addr: "client",
	}
}

// RemoteAddr implements unbufferedConn.
func (c *kpfConn) RemoteAddr() net.Addr {
	return addr{
		net:  "kubectl-port-forward",
		addr: c.remoteAddr,
	}
}
