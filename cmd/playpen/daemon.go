package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/datawire/apro/lib/logging"
	"github.com/datawire/teleproxy/pkg/supervisor"
	rpc "github.com/gorilla/rpc/v2"
	"github.com/gorilla/rpc/v2/json2"
	"github.com/pkg/errors"
)

func daemon(p *supervisor.Process) error {
	svc := new(DaemonService)
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

	// API-specific operations, via JSON-RPC
	rpcServer := rpc.NewServer()
	rpcServer.RegisterCodec(json2.NewCodec(), "application/json")
	err := rpcServer.RegisterService(svc, "daemon")
	if err != nil {
		return errors.Wrap(err, "register")
	}
	rpcServer.RegisterAfterFunc(func(i *rpc.RequestInfo) {
		p.Logf("RPC call method=%s err=%v", i.Method, i.Error)
	})
	mux.Handle(fmt.Sprintf("/api/v%d", apiVersion), rpcServer)

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

func monitorResources(p *supervisor.Process, resources []Resource) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		for _, resource := range resources {
			resource.Monitor(p)
		}

		// Wait a few seconds between loops
		select {
		case <-ticker.C:
		case <-p.Shutdown():
			return nil
		}
	}
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

func runAsDaemon() error {
	if os.Geteuid() != 0 {
		return errors.New("playpen daemon must run as root")
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

	teleproxy := "/Users/ark3/datawire/bin/pp-teleproxy-darwin-amd64"
	netOverride := NewCommandResource("netOverride",
		[]string{teleproxy, "-mode", "intercept"})
	netOverride.SetCheckFunction(func(p *supervisor.Process) error {
		// Check by doing the equivalent of curl http://teleproxy/api/tables/
		res, err := http.Get("http://teleproxy/api/tables")
		if err != nil {
			return err
		}
		_, err = ioutil.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return err
		}
		return nil
	})

	resources := []Resource{netOverride}
	sup.Supervise(&supervisor.Worker{
		Name:     "monitor",
		Requires: []string{"daemon"},
		Work: func(p *supervisor.Process) error {
			return monitorResources(p, resources)
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     "enable",
		Requires: []string{"daemon"},
		Work:     netOverride.Enable,
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
