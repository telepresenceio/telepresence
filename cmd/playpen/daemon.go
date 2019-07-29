package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/pkg/errors"

	"github.com/datawire/apro/cmd/playpen/daemon"
	"github.com/datawire/apro/lib/logging"
)

func daemonWorker(p *supervisor.Process) error {
	mux := http.NewServeMux()

	// Operations that are valid irrespective of API version (curl is okay)
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "playpen daemon %s\n", displayVersion)
	})
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			// Specifically looking to disallow GET/HEAD; requiring POST is
			// perhaps too specific, but whatever, it gets the job done.
			http.Error(w, "Bad request (use -XPOST)", 400)
			return
		}
		p.Supervisor().Shutdown()
		fmt.Fprintln(w, "Playpen Daemon quitting...")
	})

	// API-specific operations, via JSON-RPC
	svc, err := MakeDaemonService(p)
	if err != nil {
		return err
	}
	rpcServer := getRPCServer(p)
	if err := rpcServer.RegisterService(svc, "daemon"); err != nil {
		return err
	}
	mux.Handle(fmt.Sprintf("/api/v%d", apiVersion), rpcServer)

	// Listen on unix domain socket
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

	p.Ready()
	Notify(p, "Running")
	err = p.DoClean(func() error {
		if err := server.Serve(unixListener); err != http.ErrServerClosed {
			return err
		}
		return nil
	}, func() error { return server.Shutdown(p.Context()) })
	Notify(p, "Terminated")
	return err
}

func runAsDaemon() error {
	if os.Geteuid() != 0 {
		return errors.New("playpen daemon must run as root")
	}

	sup := supervisor.WithContext(context.Background())
	sup.Logger = SetUpLogging()
	sup.Supervise(&supervisor.Worker{
		Name: "daemon",
		Work: daemonWorker,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "signal",
		Requires: []string{"daemon"},
		Work:     daemon.WaitForSignal,
	})

	sup.Logger.Printf("---")
	sup.Logger.Printf("Playpen daemon %s starting...", displayVersion)
	runErrors := sup.Run()

	sup.Logger.Printf("")
	if len(runErrors) > 0 {
		sup.Logger.Printf("Daemon has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Playpen daemon %s is done.", displayVersion)
	return errors.New("playpen daemon has exited")
}
