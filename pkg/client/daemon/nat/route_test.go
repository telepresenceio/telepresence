package nat

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
)

func TestMain(m *testing.M) {
	dtest.Sudo()
	var exitCode int
	dtest.WithMachineLock(func() {
		exitCode = m.Run()
	})
	os.Exit(exitCode)
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

func checkForwardTCP(fromIP string, ports []string, toPort int) error {
	for _, port := range ports {
		from := fmt.Sprintf("%s:%s", fromIP, port)

		deadline := time.Now().Add(3 * time.Second)

		c, err := net.DialTimeout("tcp", from, 3*time.Second)
		if err != nil {
			return fmt.Errorf("unable to connect tcp %s: %v", from, err)
		}
		var actual string
		err = func() error {
			defer c.Close()
			_ = c.(*net.TCPConn).SetDeadline(deadline)

			var buf [1024]byte
			n, err := c.Read(buf[:1024])
			if err != nil {
				return fmt.Errorf("unable to read tcp %s: %v", from, err)
			}
			actual = string(buf[:n])
			return nil
		}()
		if err != nil {
			return err
		}

		expected := fmt.Sprintf("TCP %d", toPort)
		if actual != expected {
			return fmt.Errorf("connected to %s and got back %s instead of %s", from, actual, expected)
		}
	}
	return nil
}

func checkNoForwardTCP(fromIP string, ports []string) error {
	for _, port := range ports {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%s", fromIP, port), 50*time.Millisecond)
		if err != nil {
			continue
		}
		c.Close()
		return fmt.Errorf("connected to %s:%s, expecting no connection", fromIP, port)
	}
	return nil
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

type mapping struct {
	from         string
	port         string
	to           int
	forwarded    []string
	notForwarded []string
}

var mappings = []*mapping{
	{"1", "", 4321, []string{"80", "8080"}, nil},
	{"2", "", 2134, []string{"80", "8080"}, nil},
	{"3", "", 2134, []string{"80", "8080"}, nil},
	{"4", "443", 2134, []string{"443"}, []string{"80", "8080"}},
}

func testMapping(c context.Context, network string, mappings []*mapping, t *testing.T) {
	for _, mapping := range mappings {
		require.NoError(t, checkNoForwardTCP(fmt.Sprintf("%s.%s", network, mapping.from), mapping.forwarded))
	}

	tr := NewRouter("test-table", net.IP{127, 0, 1, 2}, net.IPv6loopback)
	require.NoError(t, tr.Enable(c))
	defer func() {
		_ = tr.Disable(c)
	}()

	for _, mapping := range mappings {
		from := fmt.Sprintf("%s.%s", network, mapping.from)

		require.NoError(t, checkNoForwardTCP(from, mapping.forwarded))
		ports, err := ParsePorts(mapping.port)
		require.NoError(t, err)
		route, err := NewRoute("tcp", net.ParseIP(from), ports, mapping.to)
		require.NoError(t, err)
		changed, err := tr.Add(c, route)
		require.NoError(t, err)
		assert.True(t, changed, "route wasn't added")
		require.NoError(t, tr.Flush(c))
		require.NoError(t, checkForwardTCP(from, mapping.forwarded, mapping.to))
		require.NoError(t, checkNoForwardTCP(from, mapping.notForwarded))
	}

	for _, mapping := range mappings {
		from := fmt.Sprintf("%s.%s", network, mapping.from)
		ports, err := ParsePorts(mapping.port)
		require.NoError(t, err)
		route, err := NewRoute("tcp", net.ParseIP(from), ports, mapping.to)
		require.NoError(t, err)
		changed, err := tr.Clear(c, route)
		require.NoError(t, err)
		assert.True(t, changed, "route didn't clear")
		require.NoError(t, tr.Flush(c))
		require.NoError(t, checkNoForwardTCP(from, mapping.forwarded))
	}
}

func TestTranslator(t *testing.T) {
	c, cancel := context.WithCancel(dlog.NewTestContext(t, false))
	g := dgroup.NewGroup(c, dgroup.GroupConfig{DisableLogging: true})
	g.Go("translator-test", func(c context.Context) error {
		defer cancel()
		require.NoError(t, listeners(c, []int{2134, 4321}))
		for _, env := range environments {
			env := env
			t.Run(env.testName(), func(t *testing.T) {
				require.NoError(t, env.setup(c))
				defer func() {
					require.NoError(t, env.teardown(c))
				}()
				for _, network := range networks {
					network := network
					t.Run(network, func(t *testing.T) { testMapping(c, network, mappings, t) })
				}
			})
		}
		return nil
	})
	_ = g.Wait()
}
