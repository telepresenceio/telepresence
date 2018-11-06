package proxy

import (
	"github.com/datawire/teleproxy/internal/pkg/tpu"
	"golang.org/x/net/proxy"
	"io"
	"log"
	"net"
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

func (p *Proxy) Start(limit int) {
	log.Printf("Listening (limit %v)...", limit)
	go func() {
		sem := tpu.NewSemaphore(limit)
		for {
			conn, err := p.listener.Accept()
			if err != nil {
				log.Println(err)
			} else {
				switch conn.(type) {
				case *net.TCPConn:
					log.Println("AVAILABLE:", len(sem))
					sem.Acquire()
					go func() {
						defer sem.Release()
						p.handleConnection(conn.(*net.TCPConn))
					}()
				default:
					log.Println("Don't know how to handle conn:", conn)
				}
			}
		}
	}()
}

func (p *Proxy) handleConnection(conn *net.TCPConn) {
	host, err := p.router(conn)
	if err != nil {
		log.Println("router:", err)
		return
	}

	log.Println("CONNECT:", conn.RemoteAddr(), host)

	// setting up an ssh tunnel with dynamic socks proxy at this end
	// seems faster than connecting directly to a socks proxy
	dialer, err := proxy.SOCKS5("tcp", "localhost:1080", nil, proxy.Direct)
	//	dialer, err := proxy.SOCKS5("tcp", "localhost:9050", nil, proxy.Direct)
	if err != nil {
		log.Println(err)
		conn.Close()
		return
	}

	_proxy, err := dialer.Dial("tcp", host)
	if err != nil {
		log.Println(err)
		conn.Close()
		return
	}
	proxy := _proxy.(*net.TCPConn)

	done := tpu.NewLatch(2)

	go pipe(conn, proxy, done)
	go pipe(proxy, conn, done)

	done.Wait()
}

func pipe(from, to *net.TCPConn, done tpu.Latch) {
	defer func() {
		log.Println("CLOSED WRITE:", to.RemoteAddr())
		to.CloseWrite()
	}()
	defer func() {
		log.Println("CLOSED READ:", from.RemoteAddr())
		from.CloseRead()
	}()
	defer done.Notify()

	const size = 64 * 1024
	var buf [size]byte
	for {
		n, err := from.Read(buf[0:size])
		if err != nil {
			if err != io.EOF {
				log.Println(err)
			}
			break
		} else {
			_, err := to.Write(buf[0:n])

			if err != nil {
				log.Println(err)
				break
			}
		}
	}
}
