package outbound

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/datawire/telepresence2/pkg/client/outbound/dns"
	"github.com/datawire/telepresence2/pkg/client/route"
)

type apiServer struct {
	listener net.Listener
	server   http.Server
}

func (i *interceptor) newAPIServer() (*apiServer, error) {
	handler := http.NewServeMux()
	tables := "/api/tables/"
	handler.HandleFunc(tables, func(w http.ResponseWriter, r *http.Request) {
		table := r.URL.Path[len(tables):]

		switch r.Method {
		case http.MethodGet:
			result := i.Render(table)
			if result == "" {
				http.NotFound(w, r)
			} else {
				_, _ = w.Write(append([]byte(result), '\n'))
			}
		case http.MethodPost:
			d := json.NewDecoder(r.Body)
			var table []route.Table
			err := d.Decode(&table)
			if err != nil {
				http.Error(w, err.Error(), 400)
			} else {
				for _, t := range table {
					i.Update(t)
				}
				dns.Flush()
			}
		case http.MethodDelete:
			i.Delete(table)
		}
	})
	handler.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		var paths []string
		switch r.Method {
		case http.MethodGet:
			paths = i.GetSearchPath()
			result, err := json.Marshal(paths)
			if err != nil {
				panic(err)
			} else {
				_, _ = w.Write(result)
			}
		case http.MethodPost:
			d := json.NewDecoder(r.Body)
			err := d.Decode(&paths)
			if err != nil {
				http.Error(w, err.Error(), 400)
			} else {
				i.SetSearchPath(paths)
			}
		}
	})
	handler.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Goodbye!\n"))
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			panic(err)
		}
		_ = p.Signal(os.Interrupt)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	return &apiServer{
		listener: ln,
		server: http.Server{
			Handler: handler,
		},
	}, nil
}

func (a *apiServer) Port() string {
	_, port, err := net.SplitHostPort(a.listener.Addr().String())
	if err != nil {
		panic(err)
	}
	return port
}

func (a *apiServer) Start() {
	go func() {
		if err := a.server.Serve(a.listener); err != http.ErrServerClosed {
			// Error starting or closing listener:
			log.Printf("API Server: %v", err)
		}
	}()
}

func (a *apiServer) Stop() {
	if err := a.server.Shutdown(context.Background()); err != nil {
		// Error from closing listeners, or context timeout:
		log.Printf("API Server Shutdown: %v", err)
	}
}
