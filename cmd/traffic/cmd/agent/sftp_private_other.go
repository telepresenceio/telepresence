//go:build !linux

package agent

import (
	"errors"
	"net"
)

func socketPair() (a, b net.Conn, err error) {
	return nil, nil, errors.ErrUnsupported
}
