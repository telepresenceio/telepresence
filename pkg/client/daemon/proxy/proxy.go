package proxy

import (
	"io"
	"net"

	"github.com/datawire/ambassador/pkg/supervisor"

	"github.com/datawire/ambassador/pkg/tpu"
	"golang.org/x/net/proxy"
)

type Proxy struct {
	listener net.Listener
	router   func(*net.TCPConn) (string, error)
}

func NewProxy(address string, router func(*net.TCPConn) (string, error)) (proxy *Proxy, err error) {
	tpu.Rlimit()
	ln, err := net.Listen("tcp", ":1234")
	if err == nil {
		proxy = &Proxy{ln, router}
	}
	return
}

func (pxy *Proxy) Start(p *supervisor.Process, limit int) {
	p.Logf("listening limit=%v", limit)
	go func() {
		sem := tpu.NewSemaphore(limit)
		for {
			conn, err := pxy.listener.Accept()
			if err != nil {
				p.Logf(err.Error())
			} else {
				switch conn := conn.(type) {
				case *net.TCPConn:
					p.Logf("CAPACITY: %v", len(sem))
					sem.Acquire()
					go func() {
						defer sem.Release()
						pxy.handleConnection(p, conn)
					}()
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

	done := tpu.NewLatch(2)

	go pxy.pipe(p, conn, proxy, done)
	go pxy.pipe(p, proxy, conn, done)

	done.Wait()
}

func (pxy *Proxy) pipe(p *supervisor.Process, from, to *net.TCPConn, done tpu.Latch) {
	defer func() {
		p.Logf("CLOSED WRITE %v", to.RemoteAddr())
		_ = to.CloseWrite()
	}()
	defer func() {
		p.Logf("CLOSED READ %v", from.RemoteAddr())
		_ = from.CloseRead()
	}()
	defer done.Notify()

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
