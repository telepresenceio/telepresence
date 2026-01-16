package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

type request struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Method  string            `json:"method,omitempty"`
	Body    string            `json:"body,omitempty"`
}

func main() {
	errLog := log.New(os.Stderr, "", log.LstdFlags)
	outLog := log.New(os.Stdout, "", log.LstdFlags)
	addr := os.Getenv("LISTEN_ADDRESS")
	portsEnv := os.Getenv("PORTS")
	if portsEnv == "" {
		portsEnv = os.Getenv("PORT")
	}
	if portsEnv == "" {
		portsEnv = "8080:http"
	}
	ports := strings.Split(portsEnv, ",")
	certFile, ok := os.LookupEnv("CERT_FILE")
	if !ok {
		certFile = "/certs/tls.crt"
	}
	keyFile, ok := os.LookupEnv("KEY_FILE")
	if !ok {
		keyFile = "/certs/tls.key"
	}

	servers := make([]*http.Server, 0, len(ports))
	g, ctx := errgroup.WithContext(context.Background())
	shutdownCtx, quit := context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		outLog.Print(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		outLog.Print(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		outLog.Print(r.URL.Path)
		w.WriteHeader(http.StatusOK)
		quit()
	})
	mux.HandleFunc("/forward", func(w http.ResponseWriter, r *http.Request) {
		outLog.Print(r.URL.Path)
		forwardHandler(w, r, outLog, errLog)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		outLog.Print(r.URL.Path)
		echoHandler(w, r, outLog, errLog)
	})

	for _, portAndProto := range ports {
		proto := "http"
		port := portAndProto
		if pp := strings.SplitN(portAndProto, ":", 2); len(pp) == 2 {
			port = pp[0]
			proto = pp[1]
		}
		var addrPort string
		if addr == "" {
			addrPort = ":" + port
		} else {
			addrPort = net.JoinHostPort(addr, port)
		}
		outLog.Printf("Echo server listening on %s.\n", addrPort)
		server := &http.Server{
			Addr: addrPort,
			BaseContext: func(_ net.Listener) context.Context {
				return ctx
			},
			Handler: mux,
		}
		prs := new(http.Protocols)
		prs.SetHTTP1(true)
		server.Protocols = prs
		switch proto {
		case "http":
			prs.SetUnencryptedHTTP2(true)
			g.Go(func() error {
				_ = server.ListenAndServe()
				return nil
			})
		case "https":
			prs.SetHTTP2(true)
			g.Go(func() error {
				_ = server.ListenAndServeTLS(certFile, keyFile)
				return nil
			})
		default:
			errLog.Fatalf("unknown protocol %q", proto)
		}
		servers = append(servers, server)
	}

	g.Go(func() error {
		sigstop := make(chan os.Signal, 1)
		signal.Notify(sigstop, os.Interrupt, unix.SIGTERM)
		select {
		case <-sigstop:
		case <-shutdownCtx.Done():
		}
		sdCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		var errs error
		for _, server := range servers {
			errs = errors.Join(errs, server.Shutdown(sdCtx))
		}
		return errs
	})

	err := g.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		errLog.Fatalf("Echo server exited with error: %v", err)
	}
}

func echoHandler(wr http.ResponseWriter, req *http.Request, outLog, errLog *log.Logger) {
	defer req.Body.Close()
	switch req.Method {
	case http.MethodHead:
		return
	case http.MethodGet:
	// Accepted
	default:
		wr.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	bf := &bytes.Buffer{}
	if tpID, ok := os.LookupEnv("TELEPRESENCE_INTERCEPT_ID"); ok {
		writeAndLogf(bf, outLog, "Intercept id %s\n", tpID)
		writeAndLogf(bf, outLog, "Intercepted container %q\n", os.Getenv("TELEPRESENCE_CONTAINER"))
	}
	writeAndLogf(bf, outLog, "%s %s %s\n\nHost: %s\n", req.Proto, req.Method, req.URL, req.Host)
	if len(req.Header) > 0 {
		writeAndLogf(bf, outLog, "Headers\n")
		for key, values := range req.Header {
			for _, value := range values {
				writeAndLogf(bf, outLog, "  %s: %s\n", key, value)
			}
		}
	}
	host, err := os.Hostname()
	if err == nil {
		writeAndLogf(bf, outLog, "Request served by %s\n", host)
	}
	_, _ = io.Copy(bf, req.Body)
	outLog.Printf("%s | http | %d byte(s)\n", req.RemoteAddr, bf.Len())
	hdr := wr.Header()
	hdr.Set("Content-Type", "text/plain")
	hdr.Set("Content-Length", strconv.Itoa(bf.Len()))
	_, err = bf.WriteTo(wr)
	if err != nil {
		errLog.Printf("Error serving HTTP: %v", err)
	}
}

func forwardHandler(wr http.ResponseWriter, req *http.Request, outLog, errLog *log.Logger) {
	if req.Method != http.MethodPost {
		wr.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fwReq := &request{}
	d := json.NewDecoder(req.Body)
	d.DisallowUnknownFields()
	err := d.Decode(fwReq)
	if err != nil {
		errLog.Printf("Error parsing request: %v", err)
		wr.WriteHeader(http.StatusBadRequest)
		return
	}
	var fwReqBody io.Reader
	if fwReq.Body != "" {
		fwReqBody = strings.NewReader(fwReq.Body)
	}
	if fwReq.Method == "" {
		fwReq.Method = http.MethodGet
	}
	fwr, err := http.NewRequest(fwReq.Method, os.ExpandEnv(fwReq.URL), fwReqBody)
	if err != nil {
		errLog.Printf("Error creating forward request: %v", err)
		wr.WriteHeader(http.StatusInternalServerError)
		return
	}
	for k, v := range fwReq.Headers {
		fwr.Header.Set(k, os.ExpandEnv(v))
	}
	fwRsp, err := http.DefaultClient.Do(fwr)
	if err != nil {
		errLog.Printf("Error forwarding request: %v", err)
		wr.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer fwRsp.Body.Close()

	wrh := wr.Header()
	for key, values := range fwRsp.Header {
		for _, value := range values {
			wrh.Add(key, value)
		}
	}
	if fwRsp.StatusCode != http.StatusOK {
		wr.WriteHeader(fwRsp.StatusCode)
	}
	_, err = io.Copy(wr, fwRsp.Body)
	if err != nil {
		errLog.Printf("Error forwarding response: %v", err)
	}
}

func writeAndLogf(w *bytes.Buffer, outLog *log.Logger, format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	outLog.Print(s)
	_, _ = w.WriteString(s)
}
