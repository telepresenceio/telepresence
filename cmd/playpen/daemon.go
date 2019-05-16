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

	"github.com/datawire/apro/lib/logging"
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
	mux := http.NewServeMux()

	// Operations that are valid irrespective of API version (curl is okay)
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "playpen daemon %s\n", displayVersion)
	})
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		me, err := os.FindProcess(os.Getpid())
		if err != nil {
			message := fmt.Sprintf("Error trying to quit: %v", err)
			p.Log(message)
			http.Error(w, message, http.StatusInternalServerError)
			return
		}
		me.Signal(syscall.SIGTERM)
		fmt.Fprintln(w, "Playpen Daemon quitting...")
	})

	// API-specific operations
	apiPath := fmt.Sprintf("/api/v%d", apiVersion)
	mux.HandleFunc(apiPath+"/status", makeRequestHandler(p, daemonStatus))
	mux.HandleFunc(apiPath+"/connect", makeRequestHandler(p, daemonConnect))
	mux.HandleFunc(apiPath+"/disconnect", makeRequestHandler(p, daemonDisconnect))

	unixListener, err := net.Listen("unix", socketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	err = os.Chmod(socketName, 0777)
	if err != nil {
		return errors.Wrap(err, "chmod")
	}
	server := &http.Server{
		Handler: logging.LoggingMiddleware(mux),
	}
	p.Go(func(p *supervisor.Process) error {
		err := server.Serve(unixListener)
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	})
	Notify(p, "Running")
	p.Ready()

	// Wait for Supervisor to tell us to quit
	<-p.Shutdown()

	Notify(p, "Terminated")
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
		os.Exit(1)
	}

	sup := supervisor.WithContext(context.Background())
	sup.Logger = SetUpLogging()
	sup.Supervise(&supervisor.Worker{
		Name: "daemon",
		Work: daemon,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "signal",
		Requires: []string{"daemon"},
		Work:     waitForSignal,
	})

	sup.Logger.Printf("---")
	sup.Logger.Printf("Playpen daemon %s starting...", displayVersion)
	errors := sup.Run()

	sup.Logger.Printf("")
	if len(errors) > 0 {
		sup.Logger.Printf("Daemon has exited with %d error(s):", len(errors))
		for _, err := range errors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Playpen daemon %s is done.", displayVersion)
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
