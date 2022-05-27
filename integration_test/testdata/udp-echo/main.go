package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

func main() {
	portsEnv := os.Getenv("PORTS")
	if portsEnv == "" {
		portsEnv = os.Getenv("PORT")
	}
	if portsEnv == "" {
		portsEnv = "8080"
	}
	ports := strings.Split(portsEnv, ",")
	wg := sync.WaitGroup{}
	wg.Add(len(ports))
	for _, port := range ports {
		port := port // pin it
		go func() {
			defer wg.Done()
			fmt.Printf("UDP-echo server listening on port %s.\n", port)
			defer fmt.Printf("UDP-echo server on port %s exited.\n", port)
			pc, err := net.ListenPacket("udp", ":"+port)
			if err == nil {
				err = serveConnection(pc)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
			}
		}()
	}
	wg.Wait()
}

func serveConnection(pc net.PacketConn) error {
	buf := [0x10000]byte{}
	pfx := []byte("Reply from UDP-echo: ")
	for {
		n, addr, err := pc.ReadFrom(buf[:])
		if n > 0 {
			sb := string(buf[:n])
			if strings.HasSuffix(sb, "\n") {
				fmt.Print(sb)
			} else {
				fmt.Println(sb)
			}
			if n == 5 && sb == "exit\n" {
				return nil
			}
			r := make([]byte, len(pfx)+n)
			copy(r, pfx)
			copy(r[len(pfx):], buf[:n])
			if _, werr := pc.WriteTo(r, addr); werr != nil {
				fmt.Fprintln(os.Stderr, werr.Error())
			}
		}
		if err != nil {
			return err
		}
	}
}
