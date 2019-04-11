package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/tpu"
)

type invoker struct {
	Snapshots        chan string
	mux              sync.Mutex
	invokedSnapshots map[int]string
	id               int
	notify           []string
	apiServerPort    int

	// This stores the latest snapshot, but we don't assign an id
	// unless/until we invoke... some of these will be discarded
	// by the rate limiting/coalescing logic
	latestSnapshot string
}

func NewInvoker(port int, notify []string) *invoker {
	return &invoker{
		Snapshots:        make(chan string),
		invokedSnapshots: make(map[int]string),
		notify:           notify,
		apiServerPort:    port,
	}
}

func (a *invoker) Work(p *supervisor.Process) error {
	p.Ready()
	for {
		select {
		case a.latestSnapshot = <-a.Snapshots:
			a.invoke()
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}

func (a *invoker) storeSnapshot(snapshot string) int {
	a.mux.Lock()
	defer a.mux.Unlock()
	// XXX: we should add garbage collection to
	// avoid running out of memory due to
	// snapshots
	a.id += 1
	a.invokedSnapshots[a.id] = snapshot
	return a.id
}

func (a *invoker) getSnapshot(id int) string {
	a.mux.Lock()
	defer a.mux.Unlock()
	return a.invokedSnapshots[id]
}

func (a *invoker) invoke() {
	id := a.storeSnapshot(a.latestSnapshot)
	for _, n := range a.notify {
		k := tpu.NewKeeper("notify", fmt.Sprintf("%s http://localhost:%d/snapshots/%d", n, a.apiServerPort, id))
		k.Limit = 1
		k.Start()
		k.Wait()
	}
}

type apiServer struct {
	port    int
	invoker *invoker
}

func (s *apiServer) Work(p *supervisor.Process) error {
	http.HandleFunc("/snapshots/", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/snapshots/"))
		if err != nil {
			http.Error(w, "ID is not an integer", http.StatusBadRequest)
			return
		}

		snapshot := s.invoker.getSnapshot(id)

		if snapshot == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("content-type", "application/json")
		if _, err := w.Write([]byte(snapshot)); err != nil {
			p.Logf("write snapshot error: %v", err)
		}
	})

	listenHostAndPort := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", listenHostAndPort)
	if err != nil {
		return err
	}
	defer listener.Close()
	p.Ready()
	p.Logf("snapshot server listening on: %s", listenHostAndPort)
	srv := &http.Server{
		Addr: listenHostAndPort,
	}
	// launch an anonymous child worker to serve requests
	p.Go(func(p *supervisor.Process) error {
		return srv.Serve(listener)
	})

	<-p.Shutdown()
	return srv.Shutdown(p.Context())
}
