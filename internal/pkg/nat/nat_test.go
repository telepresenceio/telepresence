package nat

import (
	"net"
	"strings"
	"testing"
	"time"
)

const (
	GOOD = "GOOD"
	BAD = "BAD"
)

func checkForwardTCP(t *testing.T, fromIP, toPort string) {
	ln, err := net.Listen("tcp", ":" + toPort)
	if err != nil {
		t.Error(err)
		return
	}
	defer ln.Close()

	deadline := time.Now().Add(3*time.Second)
	ln.(*net.TCPListener).SetDeadline(deadline)

	result := make(chan bool)
	defer func() {<-result}()

	go func() {
		defer close(result)
		conn, err := ln.Accept();
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.(*net.TCPConn).SetDeadline(deadline)

		_, orig, err := GetOriginalDst(conn.(*net.TCPConn))
		if err != nil {
			t.Error(err)
			return
		}

		if orig != fromIP {
			t.Errorf("got %s, expecting %s", orig, fromIP)
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

	c, err := net.Dial("tcp", fromIP)
	if err != nil {
		t.Error(err)
		return
	}
	defer c.Close()
	c.(*net.TCPConn).SetDeadline(deadline)

	_, err = c.Write([]byte(GOOD))
	if err != nil {
		t.Error(err)
		return
	}

	var buf [1024]byte
	n, err := c.Read(buf[:1024])
	if err != nil {
		t.Error(err)
		return
	}

	if string(buf[:n]) != GOOD {
		t.Errorf("got back %s instead of %s", buf[:n], GOOD)
	}

	t.Logf("GOOD: %s->%s", fromIP, toPort)
}

func checkNoForwardTCP(t *testing.T, fromIP string) {
	c, err := net.DialTimeout("tcp", fromIP, 1*time.Nanosecond)
	if err != nil {
		if !strings.Contains(err.Error(), "timeout") {
			t.Error(err)
		}
		return
	}
	defer c.Close()
	t.Error("created a connection")
}

/*
 *  192.0.2.0/24 (TEST-NET-1),
 *  198.51.100.0/24 (TEST-NET-2)
 *  203.0.113.0/24 (TEST-NET-3)
 */

func TestTranslator(t *testing.T) {
	tr := NewTranslator("test-table")

	checkNoForwardTCP(t, "192.0.2.1:80")
	checkNoForwardTCP(t, "192.0.2.2:80")
	checkNoForwardTCP(t, "192.0.2.3:80")

	tr.Enable()

	checkNoForwardTCP(t, "192.0.2.1:80")
	checkNoForwardTCP(t, "192.0.2.2:80")
	checkNoForwardTCP(t, "192.0.2.3:80")

	tr.ForwardTCP("192.0.2.1", "4321")
	tr.ForwardTCP("192.0.2.2", "1234")
	tr.ForwardTCP("192.0.2.3", "1234")

	checkForwardTCP(t, "192.0.2.1:80", "4321")
	checkForwardTCP(t, "192.0.2.2:80", "1234")
	checkForwardTCP(t, "192.0.2.3:80", "1234")

	tr.ClearTCP("192.0.2.3")
	checkNoForwardTCP(t, "192.0.2.2:80")

	tr.Disable()
	checkNoForwardTCP(t, "192.0.2.1:80")
	checkNoForwardTCP(t, "192.0.2.2:80")
	checkNoForwardTCP(t, "192.0.2.3:80")
	// XXX: need to make this a proper check
	ipt("-L " + tr.Name)
}
