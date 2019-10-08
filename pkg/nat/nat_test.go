package nat

import (
	"context"
	"fmt"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/ambassador/pkg/supervisor"
)

func TestMain(m *testing.M) {
	dtest.Sudo()
	dtest.WithMachineLock(func() {
		os.Exit(m.Run())
	})
}

func udp_listener(p *supervisor.Process, port int) error {
	bindaddr := fmt.Sprintf(":%d", port)
	pc, err := net.ListenPacket("udp", bindaddr)
	if err != nil {
		return err
	}
	defer pc.Close()

	p.Logf("listening on %s", bindaddr)
	p.Ready()

	return p.Do(func() error {
		for {
			var buf [1024]byte
			_, addr, err := pc.ReadFrom(buf[:])
			if err != nil {
				return err
			}
			p.Logf("got packet from %v", addr)
			_, err = pc.WriteTo([]byte(fmt.Sprintf("UDP %d", port)), addr)
			if err != nil {
				return err
			}
		}
	})
}

func tcp_listener(p *supervisor.Process, port int) error {
	bindaddr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", bindaddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	p.Logf("listening on %s", bindaddr)
	p.Ready()

	return p.Do(func() error {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return err
			}
			p.Logf("got connection from %v", conn.RemoteAddr())
			_, err = conn.Write([]byte(fmt.Sprintf("TCP %d", port)))
			conn.Close()
			if err != nil {
				return err
			}
		}
	})
}

func listeners(p *supervisor.Process, ports []int) error {
	for _, port := range ports {
		p.GoName(fmt.Sprintf("UDP-%d", port), supervisor.WorkFunc(udp_listener, port))
		p.GoName(fmt.Sprintf("TCP-%d", port), supervisor.WorkFunc(tcp_listener, port))
	}
	p.Ready()
	<-p.Shutdown()
	return nil
}

func checkForwardTCP(t *testing.T, fromIP string, ports []string, toPort string) {
	for _, port := range ports {
		from := fmt.Sprintf("%s:%s", fromIP, port)

		deadline := time.Now().Add(3 * time.Second)

		c, err := net.DialTimeout("tcp", from, 3*time.Second)
		if err != nil {
			t.Errorf("unable to connect: %v", err)
			continue
		}
		defer c.Close()
		c.(*net.TCPConn).SetDeadline(deadline)

		var buf [1024]byte
		n, err := c.Read(buf[:1024])
		if err != nil {
			t.Errorf("unable to read: %v", err)
			continue
		}

		expected := fmt.Sprintf("TCP %s", toPort)
		actual := string(buf[:n])

		if actual != expected {
			t.Errorf("connected to %s and got back %s instead of %s", from, actual, expected)
		}
	}
}

func checkNoForwardTCP(t *testing.T, fromIP string, ports []string) {
	for _, port := range ports {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%s", fromIP, port), 50*time.Millisecond)
		if err != nil {
			continue
		}
		defer c.Close()
		t.Errorf("connected to %s:%s, expecting no connection", fromIP, port)
	}
}

/*
 *  192.0.2.0/24 (TEST-NET-1),
 *  198.51.100.0/24 (TEST-NET-2)
 *  203.0.113.0/24 (TEST-NET-3)
 */

var networks = []string{
	"192.0.2",
	"198.51.100",
	"203.0.113",
}

var mappings = []struct {
	from         string
	port         string
	to           string
	forwarded    []string
	notForwarded []string
}{
	{"1", "", "4321", []string{"80", "8080"}, nil},
	{"2", "", "1234", []string{"80", "8080"}, nil},
	{"3", "", "1234", []string{"80", "8080"}, nil},
	{"4", "443", "1234", []string{"443"}, []string{"80", "8080"}},
}

func TestTranslator(t *testing.T) {
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "listeners",
		Work: supervisor.WorkFunc(listeners, []int{1234, 4321}),
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "nat",
		Requires: []string{"listeners"},
		Work: func(p *supervisor.Process) error {
			for _, env := range environments {
				env.setup()
				for _, network := range networks {
					tr := NewTranslator("test-table")

					for _, mapping := range mappings {
						checkNoForwardTCP(t, fmt.Sprintf("%s.%s", network, mapping.from), mapping.forwarded)
					}

					tr.Enable(p)

					for _, mapping := range mappings {
						from := fmt.Sprintf("%s.%s", network, mapping.from)

						checkNoForwardTCP(t, from, mapping.forwarded)
						tr.ForwardTCP(p, from, mapping.port, mapping.to)
						checkForwardTCP(t, from, mapping.forwarded, mapping.to)
						checkNoForwardTCP(t, from, mapping.notForwarded)
					}

					for _, mapping := range mappings {
						from := fmt.Sprintf("%s.%s", network, mapping.from)
						tr.ClearTCP(p, from, mapping.port)
						checkNoForwardTCP(t, from, mapping.forwarded)
					}

					tr.Disable(p)
				}
				env.teardown()
			}
			sup.Shutdown()
			return nil
		},
	})
	errs := sup.Run()
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}

}

func TestSorted(t *testing.T) {
	supervisor.MustRun("sorted", func(p *supervisor.Process) error {
		tr := NewTranslator("test-table")
		defer tr.Disable(p)
		tr.ForwardTCP(p, "192.0.2.1", "", "4321")
		tr.ForwardTCP(p, "192.0.2.3", "", "4323")
		tr.ForwardTCP(p, "192.0.2.2", "", "4322")
		tr.ForwardUDP(p, "192.0.2.4", "", "1234")
		entries := tr.sorted()
		if !reflect.DeepEqual(entries, []Entry{
			{Address{"tcp", "192.0.2.1", ""}, "4321"},
			{Address{"tcp", "192.0.2.2", ""}, "4322"},
			{Address{"tcp", "192.0.2.3", ""}, "4323"},
			{Address{"udp", "192.0.2.4", ""}, "1234"},
		}) {
			t.Errorf("not sorted: %s", entries)
		}

		return nil
	})
}
