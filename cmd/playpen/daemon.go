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

	apiPath := fmt.Sprintf("/api/v%d", apiVersion)
	mux := &SerializingMux{}
	mux.HandleSerially(apiPath+"/status", "pp", makeRequestHandler(p, daemonStatus))
	mux.HandleSerially(apiPath+"/connect", "pp", makeRequestHandler(p, daemonConnect))
	mux.HandleSerially(apiPath+"/disconnect", "pp", makeRequestHandler(p, daemonDisconnect))
	mux.HandleSerially(apiPath+"/version", "pp", makeRequestHandler(p, daemonVersion))
	mux.HandleSerially(apiPath+"/quit", "pp", makeRequestHandler(p, daemonQuit))

	unixListener, err := net.Listen("unix", socketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	server := &http.Server{
		Handler: mux,
	}
	p.Go(func(p *supervisor.Process) error {
		err := server.Serve(unixListener)
		if err != http.ErrServerClosed {
			return err
		}
		return nil
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

func daemonVersion(p *supervisor.Process, req *PPRequest) string {
	return fmt.Sprintf("playpen daemon %s\n", displayVersion)
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
