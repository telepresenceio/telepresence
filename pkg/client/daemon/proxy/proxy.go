package proxy

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/datawire/ambassador/pkg/supervisor"
	"golang.org/x/net/proxy"
)

// A Proxy listens to a port and forwards incoming connections to a router
type Proxy struct {
	listener net.Listener
	router   func(*net.TCPConn) (string, error)
}

// NewProxy returns a new Proxy instance that is listening to the given tcp address
func NewProxy(p *supervisor.Process, address string, router func(*net.TCPConn) (string, error)) (proxy *Proxy, err error) {
	setRlimit(p)
	ln, err := net.Listen("tcp", address)
	if err == nil {
		proxy = &Proxy{ln, router}
	}
	return
}

func setRlimit(p *supervisor.Process) {
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		p.Logf("error getting rlimit: %s", err.Error())
	} else {
		p.Logf("initial rlimit: %d", rLimit)
	}

	rLimit.Max = 999999
	rLimit.Cur = 999999
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		p.Logf("Error setting rlimit: %s", err.Error())
	}

	err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		p.Logf("Error getting rlimit: %s", err.Error())
	} else {
		p.Logf("Final rlimit: %d", rLimit)
	}
}

// Start starts the proxy accept loop in a separate go routine. It returns immediately
func (pxy *Proxy) Start(p *supervisor.Process, limit int32) {
	p.Logf("listening limit=%v", limit)
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
				p.Logf(err.Error())
			} else {
				atomic.AddInt32(&capacity, -1)
				connQueue <- conn
			}
		}
	}()

	go func() {
		for {
			select {
			case <-p.Shutdown():
				closing = true
				_ = pxy.listener.Close()
				return
			case conn := <-connQueue:
				cap := atomic.AddInt32(&capacity, 1)
				switch conn := conn.(type) {
				case *net.TCPConn:
					p.Logf("CAPACITY: %v", cap)
					pxy.handleConnection(p, conn)
				default:
					p.Logf("unknown connection type: %v", conn)
				}
			}
		}
	}()
}

func (pxy *Proxy) handleConnection(p *supervisor.Process, conn *net.TCPConn) {
	host, err := pxy.router(conn)
	if err != nil {
		p.Logf("router error: %v", err)
		return
	}

	p.Logf("CONNECT %s %s", conn.RemoteAddr(), host)

	// setting up an ssh tunnel with dynamic socks proxy at this end
	// seems faster than connecting directly to a socks proxy
	dialer, err := proxy.SOCKS5("tcp", "localhost:1080", nil, proxy.Direct)
	//	dialer, err := proxy.SOCKS5("tcp", "localhost:9050", nil, proxy.Direct)
	if err != nil {
		p.Log(err.Error())
		conn.Close()
		return
	}

	_proxy, err := dialer.Dial("tcp", host)
	if err != nil {
		p.Log(err.Error())
		conn.Close()
		return
	}
	proxy := _proxy.(*net.TCPConn)

	done := sync.WaitGroup{}
	done.Add(2)
	go pxy.pipe(p, conn, proxy, &done)
	go pxy.pipe(p, proxy, conn, &done)

	done.Wait()
}

func (pxy *Proxy) pipe(p *supervisor.Process, from, to *net.TCPConn, done *sync.WaitGroup) {
	defer done.Done()
	defer func() {
		p.Logf("CLOSED WRITE %v", to.RemoteAddr())
		_ = to.CloseWrite()
	}()
	defer func() {
		p.Logf("CLOSED READ %v", from.RemoteAddr())
		_ = from.CloseRead()
	}()

	const size = 64 * 1024
	var buf [size]byte
	for {
		n, err := from.Read(buf[0:size])
		if err != nil {
			if err != io.EOF {
				p.Log(err.Error())
			}
			break
		} else {
			_, err := to.Write(buf[0:n])

			if err != nil {
				p.Log(err.Error())
				break
			}
		}
	}
}
