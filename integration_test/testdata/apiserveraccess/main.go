package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/header"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

func main() {
	c, cancel := context.WithCancel(log.MakeBaseLogger(context.Background(), "DEBUG"))
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, unix.SIGTERM)
	defer func() {
		signal.Stop(sigs)
		cancel()
	}()

	go func() {
		select {
		case <-sigs:
			cancel()
		case <-c.Done():
		}
		<-sigs
		os.Exit(1)
	}()

	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c context.Context) error {
	if lv, ok := os.LookupEnv("LOG_LEVEL"); ok {
		log.SetLevel(c, lv)
	}

	ap, ok := os.LookupEnv("APP_PORT")
	if !ok {
		ap = "8080"
	}
	_, err := strconv.ParseUint(ap, 10, 16)
	if err != nil {
		return fmt.Errorf("the value %q of env APP_PORT is not a valid port number", ap)
	}

	url, err := consumeHereURL()
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", "localhost:"+ap)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/consume-here", func(w http.ResponseWriter, r *http.Request) {
		intercepted(c, url, w, r)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &dhttp.ServerConfig{Handler: mux}
	info := fmt.Sprintf("API test server on %v", ln.Addr())
	dlog.Infof(c, "%s started", info)
	defer dlog.Infof(c, "%s ended", info)
	if err := server.Serve(c, ln); err != nil && err != c.Err() {
		return fmt.Errorf("%s stopped: %w", info, err)
	}
	return nil
}

const portEnv = "TELEPRESENCE_API_PORT"
const interceptIdEnv = "TELEPRESENCE_INTERCEPT_ID"

// apiURL creates the generic URL needed to access the service
func apiURL() (string, error) {
	pe := os.Getenv(portEnv)
	if _, err := strconv.ParseUint(pe, 10, 16); err != nil {
		return "", fmt.Errorf("value %q of env %s does not represent a valid port number", pe, portEnv)
	}
	return "http://localhost:" + pe, nil
}

// consumeHereURL creates the URL for the "consume-here" endpoint
func consumeHereURL() (string, error) {
	apiURL, err := apiURL()
	if err != nil {
		return "", err
	}
	return apiURL + "/consume-here", nil
}

// consumeHere calls the consume-here endpoint with the given headers and returns the result
func consumeHere(c context.Context, url string, hm map[string]string) (bool, error) {
	rq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	rq.Header = make(http.Header, len(hm)+1)
	rq.Header.Set("X-Telepresence-Caller-Intercept-Id", os.Getenv(interceptIdEnv))
	for k, v := range hm {
		rq.Header.Set(k, v)
	}
	dlog.Debugf(c, "%s with headers\n%s", url, header.Stringer(rq.Header))
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		return false, err
	}
	defer rs.Body.Close()
	b, err := io.ReadAll(rs.Body)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(strings.TrimSpace(string(b)))
}

func intercepted(c context.Context, url string, w http.ResponseWriter, r *http.Request) {
	hm := make(map[string]string, len(r.Header))

	// The "X-With-" prefix is used as a backdoor to avoid triggering intercepts during test. It's
	// stripped off here.
	for h := range r.Header {
		hm[strings.TrimPrefix(h, "X-With-")] = r.Header.Get(h)
	}

	// The "X-Without-" prefix is used when the headers that trigger an intercept must be included in
	// order for the intercept to take place, but should be removed in the subsequent query. Both
	// the "X-Without-" headers and the header they refer to are stripped off here.
	for h := range hm {
		if hw := strings.TrimPrefix(h, "X-Without-"); h != hw {
			delete(hm, h)
			delete(hm, hw)
		}
	}
	w.Header().Set("Content-Type", "text/plain")

	if cs, err := consumeHere(c, url, hm); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to execute http request: %v", err)
	} else {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "%t", cs)
	}
}
