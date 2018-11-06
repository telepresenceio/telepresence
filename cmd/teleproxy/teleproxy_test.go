package main

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestSmoke(t *testing.T) {
	ch := make(chan struct{})
	go func() {
		main()
		close(ch)
	}()
	defer func() {
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			panic(err)
		}
		p.Signal(os.Interrupt)
		<-ch
	}()

	start := time.Now()
	for {
		resp, err := http.Get("http://teleproxied-httpbin/status/200")
		if err != nil {
			fmt.Println(err)
		} else if resp.StatusCode == 200 {
			break
		}
		time.Sleep(time.Second)
		if time.Since(start) > 30*time.Second {
			t.Errorf("time has expired")
			break
		}
	}
}
