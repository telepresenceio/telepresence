package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

type invoker struct {
	snapshotCh <-chan string
	mux        sync.Mutex
	snapshots  map[int]string
	id         int
}

func (a *invoker) Work(p *supervisor.Process) error {
	p.Ready()
	for {
		select {
		case snapshot := <-a.snapshotCh:
			id := a.storeSnapshot(snapshot)
			// XXX: we should add garbage collection to
			// avoid running out of memory due to
			// snapshots
			a.invoke(id, snapshot)
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}

func (a *invoker) storeSnapshot(snapshot string) int {
	a.mux.Lock()
	defer a.mux.Unlock()
	a.id += 1
	a.snapshots[a.id] = snapshot
	return a.id
}

func (a *invoker) getSnapshot(id int) string {
	a.mux.Lock()
	defer a.mux.Unlock()
	return a.snapshots[id]
}

func (a *invoker) invoke(id int, snapshot string) {
	fmt.Printf("invoke stub: %d, %s\n", id, snapshot)
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
