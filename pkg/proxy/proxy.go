package proxy

import (
	"io"
	"log"
	"net"

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

func (p *Proxy) log(line string, args ...interface{}) {
	log.Printf("PXY: "+line+"\n", args...)
}

func (p *Proxy) Start(limit int) {
	p.log("listening limit=%v", limit)
	go func() {
		sem := tpu.NewSemaphore(limit)
		for {
			conn, err := p.listener.Accept()
			if err != nil {
				p.log(err.Error())
			} else {
				switch conn := conn.(type) {
				case *net.TCPConn:
					p.log("CAPACITY: %v", len(sem))
					sem.Acquire()
					go func() {
						defer sem.Release()
						p.handleConnection(conn)
					}()
				default:
					p.log("unknown connection type: %v", conn)
				}
			}
		}
	}()
}

func (p *Proxy) handleConnection(conn *net.TCPConn) {
	host, err := p.router(conn)
	if err != nil {
		p.log("router error: %v", err)
		return
	}

	p.log("CONNECT %s %s", conn.RemoteAddr(), host)

	// setting up an ssh tunnel with dynamic socks proxy at this end
	// seems faster than connecting directly to a socks proxy
	dialer, err := proxy.SOCKS5("tcp", "localhost:1080", nil, proxy.Direct)
	//	dialer, err := proxy.SOCKS5("tcp", "localhost:9050", nil, proxy.Direct)
	if err != nil {
		p.log(err.Error())
		conn.Close()
		return
	}

	_proxy, err := dialer.Dial("tcp", host)
	if err != nil {
		p.log(err.Error())
		conn.Close()
		return
	}
	proxy := _proxy.(*net.TCPConn)

	done := tpu.NewLatch(2)

	go p.pipe(conn, proxy, done)
	go p.pipe(proxy, conn, done)

	done.Wait()
}

func (p *Proxy) pipe(from, to *net.TCPConn, done tpu.Latch) {
	defer func() {
		p.log("CLOSED WRITE %v", to.RemoteAddr())
		to.CloseWrite()
	}()
	defer func() {
		p.log("CLOSED READ %v", from.RemoteAddr())
		from.CloseRead()
	}()
	defer done.Notify()

	const size = 64 * 1024
	var buf [size]byte
	for {
		n, err := from.Read(buf[0:size])
		if err != nil {
			if err != io.EOF {
				p.log(err.Error())
			}
			break
		} else {
			_, err := to.Write(buf[0:n])

			if err != nil {
				p.log(err.Error())
				break
			}
		}
	}
}
