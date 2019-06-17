package dtest

import (
	"fmt"
	"net"
	"time"
)

// WithGlobalLock executes the supplied body with a guarantee that it
// is the only code running (via WithGlobalLock) on the machine.
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
			err := ln.Close()
			if err != nil {
				fmt.Println(err)
			}
		}()

		body()

		return
	}
}
