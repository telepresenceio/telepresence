package proxy

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/datawire/dlib/dlog"
	"golang.org/x/net/proxy"
)

// A Proxy listens to a port and forwards incoming connections to a router
type Proxy struct {
	listener net.Listener
	router   func(*net.TCPConn) (string, error)
}

// NewProxy returns a new Proxy instance that is listening to the given tcp address
func NewProxy(c context.Context, address string, router func(*net.TCPConn) (string, error)) (proxy *Proxy, err error) {
	setRlimit(c)
	ln, err := net.Listen("tcp", address)
	if err == nil {
		proxy = &Proxy{ln, router}
	}
	return
}

func setRlimit(c context.Context) {
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		dlog.Errorf(c, "error getting rlimit: %s", err.Error())
	} else {
		dlog.Debugf(c, "initial rlimit: %d", rLimit)
	}

	rLimit.Max = 999999
	rLimit.Cur = 999999
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		dlog.Errorf(c, "Error setting rlimit: %s", err.Error())
	}

	err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		dlog.Errorf(c, "Error getting rlimit: %s", err.Error())
	} else {
		dlog.Debugf(c, "Final rlimit: %d", rLimit)
	}
}

// Run starts the proxy accept loop and runs it until the context is cancelled
func (pxy *Proxy) Run(c context.Context, limit int32) {
	dlog.Debugf(c, "listening limit=%v", limit)
	connQueue := make(chan net.Conn, limit)
	capacity := limit
	closing := false
	go func() {
		for {
			conn, err := pxy.listener.Accept()
			if err != nil {
				if closing {
					return
				}
				dlog.Error(c, err.Error())
			} else {
				atomic.AddInt32(&capacity, -1)
				connQueue <- conn
			}
		}
	}()
	for {
		select {
		case <-c.Done():
			closing = true
			_ = pxy.listener.Close()
			return
		case conn := <-connQueue:
			cpy := atomic.AddInt32(&capacity, 1)
			switch conn := conn.(type) {
			case *net.TCPConn:
				dlog.Debugf(c, "CAPACITY: %v", cpy)
				pxy.handleConnection(c, conn)
			default:
				dlog.Errorf(c, "unknown connection type: %v", conn)
			}
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
		dlog.Error(c, err.Error())
		conn.Close()
		return
	}

	_proxy, err := dialer.Dial("tcp", host)
	if err != nil {
		dlog.Error(c, err.Error())
		conn.Close()
		return
	}
	px := _proxy.(*net.TCPConn)

	done := sync.WaitGroup{}
	done.Add(2)
	go pxy.pipe(c, conn, px, &done)
	go pxy.pipe(c, px, conn, &done)

	done.Wait()
}

func (pxy *Proxy) pipe(c context.Context, from, to *net.TCPConn, done *sync.WaitGroup) {
	defer done.Done()

	closed := int32(0)
	closePipe := func() {
		if atomic.CompareAndSwapInt32(&closed, 0, 1) {
			dlog.Debugf(c, "CLOSED WRITE %v", to.RemoteAddr())
			_ = to.CloseWrite()
			dlog.Debugf(c, "CLOSED READ %v", from.RemoteAddr())
			_ = from.CloseRead()
		}
	}
	defer closePipe()

	// Close pipes when context is done
	eop := make(chan bool)
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
				dlog.Error(c, err.Error())
			}
			break
		}
		_, err = to.Write(buf[0:n])
		if err != nil {
			dlog.Error(c, err.Error())
			break
		}
	}
	close(eop)
}
