package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// WaitForSignal is a Worker that calls Shutdown if SIGINT or SIGTERM
// is received.
func WaitForSignal(p *supervisor.Process) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	p.Ready()

	select {
	case killSignal := <-interrupt:
		switch killSignal {
		case os.Interrupt:
			p.Log("Got SIGINT...")
		case syscall.SIGTERM:
			p.Log("Got SIGTERM...")
		}
		p.Supervisor().Shutdown()
	case <-p.Shutdown():
	}
	return nil
}
