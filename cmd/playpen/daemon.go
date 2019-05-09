package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os/signal"
	"syscall"

	"fmt"
	"os"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/pkg/errors"
)

func retrieveRequest(w http.ResponseWriter, r *http.Request) *PPRequest {
	if r.Method == http.MethodPost {
		d := json.NewDecoder(r.Body)
		req := PPRequest{}
		err := d.Decode(&req)
		if err != nil {
			http.Error(w, err.Error(), 400)
		}
		return &req
	}
	http.Error(w, "Bad request", 400)
	return nil
}

func makeRequestHandler(p *supervisor.Process, handle func(*supervisor.Process, *PPRequest) string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		req := retrieveRequest(w, r)
		w.Write([]byte(handle(p, req)))
	}
}
func daemon(p *supervisor.Process) error {
	var err error

	mux := &SerializingMux{}
	mux.HandleSerially("/status", "pp", makeRequestHandler(p, daemonStatus))
	mux.HandleSerially("/connect", "pp", makeRequestHandler(p, daemonConnect))
	mux.HandleSerially("/disconnect", "pp", makeRequestHandler(p, daemonDisconnect))
	mux.HandleSerially("/version", "pp", makeRequestHandler(p, daemonVersion))
	mux.HandleSerially("/quit", "pp", makeRequestHandler(p, daemonQuit))

	unixListener, err := net.Listen("unix", socketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	server := &http.Server{
		Handler: mux,
	}
	p.Go(func(p *supervisor.Process) error {
		return server.Serve(unixListener)
	})
	p.Ready()

	// Wait for Supervisor to tell us to quit
	<-p.Shutdown()
	return server.Shutdown(p.Context())
}

func waitForSignal(p *supervisor.Process) error {
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

func runAsDaemon() {
	if os.Geteuid() != 0 {
		fmt.Println("Playpen Daemon must run as root.")
		//os.Exit(1)
	}

	sup := supervisor.WithContext(context.Background())
	//sup.Logger = ...
	sup.Supervise(&supervisor.Worker{
		Name: "daemon",
		Work: daemon,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "signal",
		Requires: []string{"daemon"},
		Work:     waitForSignal,
	})

	errors := sup.Run()
	sup.Logger.Printf("Daemon has exited")
	for _, err := range errors {
		sup.Logger.Printf("- %v", err)
	}
	sup.Logger.Printf("Daemon is done.")
	os.Exit(1)
}

func daemonStatus(p *supervisor.Process, req *PPRequest) string {
	return "Not connected"
}

func daemonConnect(p *supervisor.Process, req *PPRequest) string {
	return "Not implemented..."
}

func daemonDisconnect(p *supervisor.Process, req *PPRequest) string {
	return "Not connected"
}

func daemonVersion(p *supervisor.Process, req *PPRequest) string {
	return fmt.Sprintf("playpen daemon v%s (api v%d)\n", Version, apiVersion)
}

func daemonQuit(p *supervisor.Process, req *PPRequest) string {
	me, err := os.FindProcess(os.Getpid())
	if err != nil {
		message := fmt.Sprintf("Error trying to quit: %v", err)
		p.Log(message)
		return message
	}
	me.Signal(syscall.SIGTERM)
	return "Playpen Daemon quitting..."
}
