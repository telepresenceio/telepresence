package dnet_test

import (
	"net"

	"golang.org/x/net/nettest"
)

func flipMakePipe(mp nettest.MakePipe) nettest.MakePipe {
	return func() (c1, c2 net.Conn, stop func(), err error) {
		c2, c1, stop, err = mp()
		return
	}
}
