package restapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
)

const (
	HeaderCallerInterceptID = "X-Telepresence-Caller-Intercept-Id"
	HeaderInterceptID       = "X-Telepresence-Intercept-Id"
	EndPointConsumeHere     = "/consume-here"
	EndPointInterceptInfo   = "/intercept-info"
)

type InterceptInfo struct {
	// True if the service is being intercepted
	Intercepted bool `json:"intercepted"`

	// True when queried on the workstation side, false if it is the cluster side agent.
	ClientSide bool `json:"clientSide"`

	// Metadata associated with the intercept. Only available on when Intercepted == ClientSide
	Metadata map[string]string `json:"metadata,omitempty"`
}

type AgentState interface {
	// InterceptInfo returns information about an ongoing intercept that matches
	// the given arguments.
	InterceptInfo(ctx context.Context, callerID, path string, containerPort uint16, headers http.Header) (*InterceptInfo, error)
}

type Server interface {
	ListenAndServe(context.Context, int) error
	Serve(context.Context, net.Listener) error
}

type ErrorResponse struct {
	Error string `json:"error,omitempty"`
}

func NewServer(agent AgentState) Server {
	return &server{
		agent: agent,
	}
}

type server struct {
	agent AgentState
}

// ListenAndServe is like Serve but creates a TCP listener on "localhost:<apiPort>".
func (s *server) ListenAndServe(c context.Context, apiPort int) error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(apiPort))
	if err != nil {
		return err
	}
	return s.Serve(c, ln)
}

func (s *server) interceptInfo(c context.Context, p string, cp uint16, h http.Header) (*InterceptInfo, error) {
	ii, err := s.agent.InterceptInfo(c, h.Get(HeaderCallerInterceptID), p, cp, h)
	if err != nil {
		return nil, err
	}
	clog.Debugf(c, "InterceptInfo(path: %s, port %d, header %s => %v", p, cp, matcher.HeaderStringer(h), ii)
	return ii, nil
}

// Serve starts the API server. It terminates when the given context is done.
func (s *server) Serve(c context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	writeError := func(w http.ResponseWriter, status int, err error) {
		w.WriteHeader(status)
		if err := json.MarshalWrite(w, &ErrorResponse{Error: err.Error()}); err != nil {
			clog.Errorf(c, "error %v when responding with error %v", err, err)
		}
	}

	containerPort := func(w http.ResponseWriter, r *http.Request) (uint16, bool) {
		if cpv := r.FormValue("containerPort"); cpv != "" {
			i, err := strconv.ParseUint(cpv, 10, 16)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("containerPort: %w", err))
				return 0, false
			}
			return uint16(i), true
		}
		return 0, true
	}

	mux.HandleFunc(EndPointConsumeHere, func(w http.ResponseWriter, r *http.Request) {
		clog.Debugf(c, "Received %s", EndPointConsumeHere)
		w.Header().Set("Content-Type", "application/json")
		cp, ok := containerPort(w, r)
		if !ok {
			return
		}
		if ii, err := s.interceptInfo(c, r.FormValue("path"), cp, r.Header); err != nil {
			writeError(w, http.StatusInternalServerError, err)
		} else {
			// Client must consume intercepted messages. Agent must not.
			consumeHere := ii.Intercepted
			if !ii.ClientSide {
				consumeHere = !consumeHere
			}
			if err = json.MarshalWrite(w, consumeHere); err != nil {
				clog.Errorf(c, "error %v when responding with %t", err, consumeHere)
			}
		}
	})
	mux.HandleFunc(EndPointInterceptInfo, func(w http.ResponseWriter, r *http.Request) {
		clog.Debugf(c, "Received %s", EndPointInterceptInfo)
		w.Header().Set("Content-Type", "application/json")
		cp, ok := containerPort(w, r)
		if !ok {
			return
		}
		if ii, err := s.interceptInfo(c, r.FormValue("path"), cp, r.Header); err != nil {
			writeError(w, http.StatusInternalServerError, err)
		} else if err = json.MarshalWrite(w, &ii); err != nil {
			clog.Errorf(c, "error %v when responding with %v", err, ii)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	svc := http.Server{
		Handler:     mux,
		BaseContext: func(_ net.Listener) context.Context { return c },
	}

	info := fmt.Sprintf("Telepresence API server on %v", ln.Addr())
	go func() {
		clog.Infof(c, "%s started", info)
		defer clog.Infof(c, "%s ended", info)
		err := svc.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			clog.Errorf(c, "%s stopped. %v", info, err)
		}
	}()

	<-c.Done()
	clog.Infof(c, "Shutting down %s", info)
	sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := svc.Shutdown(sc)
	cancel()
	return err
}
