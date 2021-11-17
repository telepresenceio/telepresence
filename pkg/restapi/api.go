package restapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
)

const HeaderCallerInterceptID = "x-telepresence-caller-intercept-id"
const HeaderInterceptID = "x-telepresence-intercept-id"
const EndPontConsumeHere = "/consume-here"

type AgentState interface {
	// Intercepts returns true if the agent currently intercepts the given Header
	Intercepts(context.Context, string, http.Header) (bool, error)
}

type Server interface {
	ListenAndServe(context.Context, int) error
	Serve(context.Context, net.Listener) error
}

type ErrorResponse struct {
	Error string `json:"error,omitempty"`
}

func NewServer(agent AgentState, client bool) Server {
	return &server{
		agent:  agent,
		client: client,
	}
}

type server struct {
	agent  AgentState
	client bool
}

// ListenAndServe is like Serve but creates a TCP listener on "localhost:<apiPort>"
func (s *server) ListenAndServe(c context.Context, apiPort int) error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(apiPort))
	if err != nil {
		return err
	}
	return s.Serve(c, ln)
}

func (s *server) intercepts(c context.Context, h http.Header) (bool, error) {
	return s.agent.Intercepts(c, h.Get(HeaderCallerInterceptID), h)
}

// Serve starts the API server. It terminates when the given context is done.
func (s *server) Serve(c context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc(EndPontConsumeHere, func(w http.ResponseWriter, r *http.Request) {
		dlog.Debugf(c, "Received %s", EndPontConsumeHere)
		w.Header().Set("Content-Type", "text/plain")
		intercepted, err := s.intercepts(c, r.Header)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			if _, wErr := w.Write([]byte(err.Error())); wErr != nil {
				dlog.Errorf(c, "error %v when responding with error %v", wErr, err)
			}
		} else {
			// Client must consume intercepted messages. Agent must not.
			consumeHere := intercepted
			if !s.client {
				consumeHere = !consumeHere
			}
			if _, wErr := w.Write([]byte(strconv.FormatBool(consumeHere))); wErr != nil {
				dlog.Errorf(c, "error %v when responding with %t", wErr, consumeHere)
			}
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &dhttp.ServerConfig{Handler: mux}
	info := fmt.Sprintf("Telepresnece API server on %v", ln.Addr())
	dlog.Infof(c, "%s started", info)
	defer dlog.Infof(c, "%s ended", info)
	if err := server.Serve(c, ln); err != nil && err != c.Err() {
		return fmt.Errorf("%s stopped. %w", info, err)
	}
	return nil
}
