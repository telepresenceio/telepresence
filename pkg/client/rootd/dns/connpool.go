package dns

import (
	"container/heap"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"
)

type ConnPool struct {
	items      map[*dns.Conn]bool
	newArrival chan *waitingClient
	finished   chan *dns.Conn
	clients    clientQueue
	cancel     context.CancelFunc
	remoteAddr string
}

func NewConnPool(addr string, poolSize int) (*ConnPool, error) {
	cCtx, cCancel := context.WithCancel(context.Background())
	pool := &ConnPool{
		items:      make(map[*dns.Conn]bool, poolSize),
		newArrival: make(chan *waitingClient),
		finished:   make(chan *dns.Conn),
		cancel:     cCancel,
		remoteAddr: addr,
	}
	heap.Init(&pool.clients)
	for i := 0; i < poolSize; i++ {
		conn, err := dns.Dial("udp", net.JoinHostPort(addr, "53"))
		if err != nil {
			return nil, fmt.Errorf("unable to create DNS conn to %s: %w", addr, err)
		}
		pool.items[conn] = false
	}
	go pool.coordinate(cCtx)
	return pool, nil
}

func (cp *ConnPool) LocalAddrs() []*net.UDPAddr {
	retval := make([]*net.UDPAddr, len(cp.items))
	i := 0
	for conn := range cp.items {
		retval[i] = conn.LocalAddr().(*net.UDPAddr)
		i++
	}
	return retval
}

func (cp *ConnPool) RemoteAddr() string {
	return cp.remoteAddr
}

func (cp *ConnPool) Exchange(ctx context.Context, client *dns.Client, msg *dns.Msg) (r *dns.Msg, rtt time.Duration, err error) {
	conn, err := cp.getConnection(ctx)
	if err != nil {
		return nil, time.Duration(0), err
	}
	defer cp.releaseConnection(conn)
	return client.ExchangeWithConn(msg, conn)
}

func (cp *ConnPool) Close() {
	cp.cancel()
	for conn := range cp.items {
		conn.Close()
	}
}

func (cp *ConnPool) coordinate(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case client := <-cp.newArrival:
			heap.Push(&cp.clients, client)
		case conn := <-cp.finished:
			cp.items[conn] = false
		}
		for conn, inUse := range cp.items {
			if !inUse && len(cp.clients) > 0 {
				cp.items[conn] = true
				client := heap.Pop(&cp.clients).(*waitingClient)
				select {
				case client.returnCh <- conn:
				case <-client.doneCh:
					cp.items[conn] = false
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func (cp *ConnPool) getConnection(ctx context.Context) (*dns.Conn, error) {
	client := &waitingClient{
		arrivalTime: time.Now(),
		returnCh:    make(chan *dns.Conn),
		doneCh:      ctx.Done(),
	}
	select {
	case cp.newArrival <- client:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case conn := <-client.returnCh:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (cp *ConnPool) releaseConnection(conn *dns.Conn) {
	cp.finished <- conn
}
