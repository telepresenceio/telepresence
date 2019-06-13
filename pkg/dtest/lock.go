package dtest

import (
	"fmt"
	"net"
	"time"
)

func WithGlobalLock(body func()) {
	// any fixed free port will work
	port := 1025
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
