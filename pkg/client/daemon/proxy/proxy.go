package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/sync/semaphore"

	"github.com/datawire/dlib/dlog"
)

// A Proxy listens to a port and forwards incoming connections to a router
type Proxy struct {
	listener    net.Listener
	connHandler func(*Proxy, context.Context, *net.TCPConn)
	router      func(*net.TCPConn) (string, error)
}

// NewProxy returns a new Proxy instance that is listening to the given tcp address
func NewProxy(c context.Context, router func(*net.TCPConn) (string, error)) (proxy *Proxy, err error) {
	setRlimit(c)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		proxy = &Proxy{listener: ln, connHandler: (*Proxy).handleConnection, router: router}
	}
	return
}

func setRlimit(c context.Context) {
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		dlog.Errorf(c, "error getting rlimit: %v", err)
	} else {
		dlog.Debugf(c, "initial rlimit: %d", rLimit)
	}

	rLimit.Max = 999999
	rLimit.Cur = 999999
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		dlog.Errorf(c, "Error setting rlimit: %v", err)
	}

	err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		dlog.Errorf(c, "Error getting rlimit: %v", err)
	} else {
		dlog.Debugf(c, "Final rlimit: %d", rLimit)
	}
}

// ListenerPort returns the port that this proxy listens to
func (pxy *Proxy) ListenerPort() (int, error) {
	_, portString, err := net.SplitHostPort(pxy.listener.Addr().String())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(portString)
}

// Run starts the proxy accept loop and runs it until the context is cancelled. The limit argument
// limits the maximum number of connections that can be concurrently proxied. This limit prevents
// exhausting all available file descriptors when clients are greedy about opening connections. This
// was originally encountered with load testing clients. You might argue this is a bug in those
// clients, and you might be right, but without this limit it manifests in a far more confusing way
// as a bug in telepresence.
func (pxy *Proxy) Run(c context.Context, limit int64) {
	dlog.Debugf(c, "proxy limit=%v", limit)
	// This semaphore tracks how many more connections we can proxy without exceeding the concurrent
	// connection limit.
	capacity := semaphore.NewWeighted(limit)
	dlog.Debugf(c, "Listening to %s", pxy.listener.Addr())

	// Ensure that listener is closed when context is done
	go func() {
		<-c.Done()
		_ = pxy.listener.Close()
	}()

	for {
		conn, err := pxy.listener.Accept()
		if err != nil {
			if c.Err() != nil {
				// Context done or cancelled, so error is very likely
				// caused by a listener close
				return
			}
			dlog.Error(c, err)
			continue
		}
		switch conn := conn.(type) {
		case *net.TCPConn:
			dlog.Debugf(c, "Handling connection from %s", conn.RemoteAddr())
			if err = capacity.Acquire(c, 1); err != nil {
				dlog.Errorf(c, "proxy failed to acquire semaphore: %v", err)
				return
			}
			go func() {
				defer capacity.Release(1)
				pxy.connHandler(pxy, c, conn)
				dlog.Debugf(c, "Done handling connection from %s", conn.RemoteAddr())
			}()
		default:
			dlog.Errorf(c, "unknown connection type: %v", conn)
		}
	}
}

func (pxy *Proxy) handleConnection(c context.Context, conn *net.TCPConn) {
	host, err := pxy.router(conn)
	if err != nil {
		dlog.Errorf(c, "router error: %v", err)
		return
	}

	dlog.Debugf(c, "CONNECT %s %s", conn.RemoteAddr(), host)

	// setting up an ssh tunnel with dynamic socks proxy at this end
	// seems faster than connecting directly to a socks proxy
	dialer, err := proxy.SOCKS5("tcp", "localhost:1080", nil, proxy.Direct)
	//	dialer, err := proxy.SOCKS5("tcp", "localhost:9050", nil, proxy.Direct)
	if err != nil {
		dlog.Error(c, err)
		conn.Close()
		return
	}

	dlog.Debugf(c, "SOCKS5 DialContext %s -> %s", "localhost:1080", host)
	tc, cancel := context.WithTimeout(c, 5*time.Second)
	defer cancel()
	px, err := dialer.(proxy.ContextDialer).DialContext(tc, "tcp", host)
	if err != nil {
		if tc.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timeout when dialing tcp %s", host)
		}
		dlog.Error(c, err)
		conn.Close()
		return
	}

	done := sync.WaitGroup{}
	done.Add(2)
	go pxy.pipe(c, conn, px, &done)
	go pxy.pipe(c, px, conn, &done)

	done.Wait()
}

func (pxy *Proxy) pipe(c context.Context, from, to net.Conn, done *sync.WaitGroup) {
	defer done.Done()

	closed := int32(0)
	closePipe := func() {
		if atomic.CompareAndSwapInt32(&closed, 0, 1) {
			dlog.Debugf(c, "CLOSED %v -> %v", from.LocalAddr(), from.RemoteAddr())
			_ = from.Close()
		}
	}
	defer closePipe()

	// Close pipes when context is done
	eop := make(chan bool)
	defer close(eop)

	go func() {
		select {
		case <-eop:
			// just end this goroutine
		case <-c.Done():
			// close the pipe
			closePipe()
		}
	}()

	const size = 64 * 1024
	var buf [size]byte
	for {
		n, err := from.Read(buf[0:size])
		if err != nil {
			if err != io.EOF {
				dlog.Error(c, err)
			}
			break
		}
		_, err = to.Write(buf[0:n])
		if err != nil {
			dlog.Error(c, err)
			break
		}
	}
}
