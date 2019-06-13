package dtest

import (
	"fmt"
	"net"
	"time"
)

const GLOBAL = 1025

func WithPortlock(port int, body func()) {
	for {
		ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
		if err != nil {
			fmt.Println(err)
			time.Sleep(1 * time.Second)
			continue
		}

		defer func() {
			ln.Close()
		}()

		body()

		return
	}
}
