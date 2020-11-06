package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/datawire/ambassador/pkg/dlog"
	"github.com/sethvargo/go-envconfig"
	"golang.org/x/sync/errgroup"

	"github.com/datawire/telepresence2/pkg/version"
)

type MechConfig struct {
	AgentPort   int    `env:"AGENT_PORT,required"`
	AppPort     int    `env:"APP_PORT,required"`
	Mechanism   string `env:"MECHANISM,required"`
	ManagerHost string `env:"MANAGER_HOST,required"`
}

func mech_tcp_main() {
	// Log plainly to stderr. The output will show up in the Agent's logs.
	log.SetFlags(0)
	log.SetPrefix("tcp: ")
	log.SetOutput(os.Stderr)

	if version.Version == "" {
		version.Version = "(devel)"
	}

	log.Printf("Mechanism TCP %s [pid:%d]", version.Version, os.Getpid())

	g, ctx := errgroup.WithContext(context.Background())

	// Handle configuration
	config := MechConfig{}
	if err := envconfig.Process(ctx, &config); err != nil {
		dlog.Error(ctx, err)
		os.Exit(1)
	}
	log.Printf("%+v", config)

	// Perform dumb forwarding AgentPort -> AppPort
	g.Go(func() error {
		lisAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", config.AgentPort))
		if err != nil {
			return err
		}

		appAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", config.AppPort))
		if err != nil {
			return err
		}

		listener, err := net.ListenTCP("tcp", lisAddr)
		if err != nil {
			return err
		}
		defer listener.Close()

		for {
			conn, err := listener.AcceptTCP()
			if err != nil {
				log.Printf("Error on accept: %+v", err)
				continue
			}
			go forwardConn(ctx, appAddr, conn)
		}
	})

	// Handle shutdown
	g.Go(func() error {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

		select {
		case sig := <-sigs:
			log.Printf("Shutting down due to signal %v", sig)
			return fmt.Errorf("received signal %v", sig)
		case <-ctx.Done():
			return nil
		}
	})

	// Wait for exit
	if err := g.Wait(); err != nil {
		log.Printf("quit: %v", err)
		os.Exit(1)
	}
}

func forwardConn(ctx context.Context, addr *net.TCPAddr, src *net.TCPConn) {
	log.Printf("Forwarding for %s to %s", src.RemoteAddr(), addr)
	defer log.Printf("Done forwarding for %s to %s", src.RemoteAddr(), addr)

	defer src.Close()

	dst, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		log.Printf("Error on dial(%s): %+v", addr, err)
		return
	}
	defer dst.Close()

	done := make(chan struct{})

	go func() {
		if _, err := io.Copy(dst, src); err != nil {
			log.Printf("Error src->dst (%s): %+v", addr, err)
		}
		_ = dst.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(src, dst); err != nil {
			log.Printf("Error dst->src (%s): %+v", addr, err)
		}
		_ = src.CloseWrite()
		done <- struct{}{}
	}()

	// Wait for both sides to close the connection
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return
		case <-done:
		}
	}
}
