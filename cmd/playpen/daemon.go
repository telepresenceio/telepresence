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

func daemon(p *supervisor.Process) error {
	var err error

	mux := &SerializingMux{}
	mux.HandleSerially("/", "pp", func(w http.ResponseWriter, r *http.Request) {
		req := retrieveRequest(w, r)
		w.Write([]byte(fmt.Sprintf("Got %s request at path %s.", req.Command, r.URL.Path)))
	})
	mux.HandleSerially("/status", "pp", func(w http.ResponseWriter, r *http.Request) {
		req := retrieveRequest(w, r)
		w.Write([]byte(daemonStatus(p, req)))
	})
	mux.HandleSerially("/connect", "pp", func(w http.ResponseWriter, r *http.Request) {
		req := retrieveRequest(w, r)
		w.Write([]byte(daemonConnect(p, req)))
	})
	mux.HandleSerially("/disconnect", "pp", func(w http.ResponseWriter, r *http.Request) {
		req := retrieveRequest(w, r)
		w.Write([]byte(daemonDisconnect(p, req)))
	})
	mux.HandleSerially("/version", "pp", func(w http.ResponseWriter, r *http.Request) {
		req := retrieveRequest(w, r)
		w.Write([]byte(daemonVersion(p, req)))
	})
	mux.HandleSerially("/quit", "pp", func(w http.ResponseWriter, r *http.Request) {
		req := retrieveRequest(w, r)
		w.Write([]byte(daemonQuit(p, req)))
	})

	unixListener, err := net.Listen("unix", socketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}

	server := &http.Server{
		Handler: mux,
	}

	serverErr := make(chan error)
	go func() {
		serverErr <- server.Serve(unixListener)
	}()

	p.Ready()

	select {
	case err = <-serverErr: // Server failed
		err = errors.Wrap(err, "server failed")
		p.Supervisor().Shutdown()
	case <-p.Shutdown(): // Supervisor told us to quit
		err = errors.Wrap(server.Shutdown(p.Context()), "shutting down server")
	}
	return err
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
