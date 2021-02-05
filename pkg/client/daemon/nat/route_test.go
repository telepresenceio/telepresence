package nat

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
)

func TestMain(m *testing.M) {
	dtest.Sudo()
	dtest.WithMachineLock(func() {
		os.Exit(m.Run())
	})
}

func udpListener(c context.Context, port int) error {
	bindaddr := fmt.Sprintf(":%d", port)
	pc, err := net.ListenPacket("udp", bindaddr)
	if err != nil {
		return err
	}

	dlog.Infof(c, "listening on %s", bindaddr)
	g := dgroup.ParentGroup(c)
	g.Go(fmt.Sprintf("UDP-%d", port), func(c context.Context) error {
		defer pc.Close()
		done := false
		addrs := make(chan net.Addr)
		go func() {
			for {
				var buf [1024]byte
				_, addr, err := pc.ReadFrom(buf[:])
				if done {
					return
				}
				if err != nil {
					dlog.Error(c, err)
					return
				}
				addrs <- addr
			}
		}()
		for {
			select {
			case <-c.Done():
				done = true
				return nil
			case addr := <-addrs:
				dlog.Debugf(c, "got packet from %v", addr)
				_, err = pc.WriteTo([]byte(fmt.Sprintf("UDP %d", port)), addr)
				if err != nil {
					dlog.Error(c, err)
					return err
				}
			}
		}
	})
	return nil
}

func tcpListener(c context.Context, port int) error {
	bindaddr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", bindaddr)
	if err != nil {
		return err
	}

	dlog.Infof(c, "listening on %s", bindaddr)
	g := dgroup.ParentGroup(c)
	g.Go(fmt.Sprintf("TCP-%d", port), func(c context.Context) error {
		defer ln.Close()
		done := false
		conns := make(chan net.Conn)
		go func() {
			for {
				conn, err := ln.Accept()
				if done {
					return
				}
				if err != nil {
					dlog.Error(c, err)
					return
				}
				conns <- conn
			}
		}()

		for {
			select {
			case <-c.Done():
				done = true
				return nil
			case conn := <-conns:
				dlog.Infof(c, "got connection from %v", conn.RemoteAddr())
				_, err = conn.Write([]byte(fmt.Sprintf("TCP %d", port)))
				conn.Close()
				if err != nil {
					dlog.Error(c, err)
					return err
				}
			}
		}
	})
	return nil
}

func listeners(c context.Context, ports []int) (err error) {
	for _, port := range ports {
		if err = udpListener(c, port); err != nil {
			return err
		}
		if err = tcpListener(c, port); err != nil {
			return err
		}
	}
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
		_ = c.(*net.TCPConn).SetDeadline(deadline)

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
	{"2", "", "2134", []string{"80", "8080"}, nil},
	{"3", "", "2134", []string{"80", "8080"}, nil},
	{"4", "443", "2134", []string{"443"}, []string{"80", "8080"}},
}

func testGroup() (*dgroup.Group, func()) {
	c, cancel := context.WithCancel(context.Background())
	logger := logrus.StandardLogger()
	logger.Level = logrus.WarnLevel
	c = dlog.WithLogger(c, dlog.WrapLogrus(logger))
	return dgroup.NewGroup(c, dgroup.GroupConfig{DisableLogging: true}), cancel
}

func TestTranslator(t *testing.T) {
	g, cancel := testGroup()
	g.Go("translator-test", func(c context.Context) error {
		err := listeners(c, []int{2134, 4321})
		if err != nil {
			t.Fatal(err)
		}
		for _, env := range environments {
			err = env.setup(c)
			if err != nil {
				t.Fatal(err)
			}
			for _, network := range networks {
				tr := NewRouter("test-table")

				for _, mapping := range mappings {
					checkNoForwardTCP(t, fmt.Sprintf("%s.%s", network, mapping.from), mapping.forwarded)
				}

				err = tr.Enable(c)
				if err != nil {
					t.Fatal(err)
				}

				for _, mapping := range mappings {
					from := fmt.Sprintf("%s.%s", network, mapping.from)

					checkNoForwardTCP(t, from, mapping.forwarded)
					if err = tr.ForwardTCP(c, from, mapping.port, mapping.to); err != nil {
						t.Fatal(err)
					}
					checkForwardTCP(t, from, mapping.forwarded, mapping.to)
					checkNoForwardTCP(t, from, mapping.notForwarded)
				}

				for _, mapping := range mappings {
					from := fmt.Sprintf("%s.%s", network, mapping.from)
					if err = tr.ClearTCP(c, from, mapping.port); err != nil {
						t.Fatal(err)
					}
					checkNoForwardTCP(t, from, mapping.forwarded)
				}

				if err = tr.Disable(c); err != nil {
					t.Fatal(err)
				}
			}
			err = env.teardown(c)
			if err != nil {
				t.Fatal(err)
			}
		}
		cancel()
		return nil
	})
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}
