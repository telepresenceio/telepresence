package nat

import (
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	GOOD = "GOOD"
	BAD  = "BAD"
)

func checkForwardTCP(t *testing.T, tr *Translator, fromIP string, ports []string, toPort string) {
	ln, err := net.Listen("tcp", ":"+toPort)
	if err != nil {
		t.Error(err)
		return
	}
	defer ln.Close()

	for _, port := range ports {
		from := fmt.Sprintf("%s:%s", fromIP, port)

		deadline := time.Now().Add(3 * time.Second)
		ln.(*net.TCPListener).SetDeadline(deadline)

		result := make(chan bool)
		defer func() { <-result }()

		go func() {
			defer close(result)
			conn, err := ln.Accept()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			conn.(*net.TCPConn).SetDeadline(deadline)

			_, orig, err := tr.GetOriginalDst(conn.(*net.TCPConn))
			if err != nil {
				t.Error(err)
				return
			}

			if orig != from {
				t.Errorf("got %s, expecting %s", orig, from)
			}

			var buf [1024]byte
			n, err := conn.Read(buf[0:1024])
			if err != nil {
				t.Error(err)
				return
			}

			_, err = conn.Write(buf[0:n])
			if err != nil {
				t.Error(err)
				return
			}
		}()

		c, err := net.DialTimeout("tcp", from, 3*time.Second)
		if err != nil {
			t.Error(err)
			continue
		}
		defer c.Close()
		c.(*net.TCPConn).SetDeadline(deadline)

		_, err = c.Write([]byte(GOOD))
		if err != nil {
			t.Error(err)
			continue
		}

		var buf [1024]byte
		n, err := c.Read(buf[:1024])
		if err != nil {
			t.Error(err)
			continue
		}

		if string(buf[:n]) != GOOD {
			t.Errorf("got back %s instead of %s", buf[:n], GOOD)
		}

		t.Logf("GOOD: %s->%s", from, toPort)
	}
}

func checkNoForwardTCP(t *testing.T, fromIP string, ports []string) {
	for _, port := range ports {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%s", fromIP, port), 1*time.Nanosecond)
		if err != nil {
			if !strings.Contains(err.Error(), "timeout") {
				t.Error(err)
			}
			return
		}
		defer c.Close()
		t.Error("created a connection")
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
	from string
	to   string
}{
	{"1", "4321"},
	{"2", "1234"},
	{"3", "1234"},
}

var ports = []string{"80", "8080"}

func TestTranslator(t *testing.T) {
	for _, env := range environments {
		env.setup()
		for _, network := range networks {
			tr := NewTranslator("test-table")

			for _, mapping := range mappings {
				checkNoForwardTCP(t, fmt.Sprintf("%s.%s", network, mapping.from), ports)
			}

			tr.Enable()

			for _, mapping := range mappings {
				from := fmt.Sprintf("%s.%s", network, mapping.from)

				checkNoForwardTCP(t, from, ports)
				tr.ForwardTCP(from, mapping.to)
				checkForwardTCP(t, tr, from, ports, mapping.to)
			}

			for _, mapping := range mappings {
				from := fmt.Sprintf("%s.%s", network, mapping.from)
				tr.ClearTCP(from)
				checkNoForwardTCP(t, from, ports)
			}

			tr.Disable()
		}
		env.teardown()
	}
}

func TestSorted(t *testing.T) {
	tr := NewTranslator("test-table")
	defer tr.Disable()
	tr.ForwardTCP("192.0.2.1", "4321")
	tr.ForwardTCP("192.0.2.3", "4323")
	tr.ForwardTCP("192.0.2.2", "4322")
	tr.ForwardUDP("192.0.2.4", "1234")
	entries := tr.sorted()
	if !reflect.DeepEqual(entries, []Entry{
		{Address{"tcp", "192.0.2.1"}, "4321"},
		{Address{"tcp", "192.0.2.2"}, "4322"},
		{Address{"tcp", "192.0.2.3"}, "4323"},
		{Address{"udp", "192.0.2.4"}, "1234"},
	}) {
		t.Errorf("not sorted: %s", entries)
	}
}
