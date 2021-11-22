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

	"github.com/sethvargo/go-envconfig"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/header"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type Env struct {
	AppPort  int    `env:"APP_PORT"`
	APIPort  int    `env:"TELEPRESENCE_API_PORT"`
	LogLevel string `env:"LOG_LEVEL,default="`
}

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
	env := Env{}
	if err := envconfig.Process(c, &env); err != nil {
		return err
	}
	if env.LogLevel != "" {
		log.SetLevel(c, env.LogLevel)
	}

	ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(env.AppPort))
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	if env.APIPort != 0 {
		apiURL := "http://localhost:" + strconv.Itoa(env.APIPort)
		mux.HandleFunc(restapi.EndPontConsumeHere, func(w http.ResponseWriter, r *http.Request) {
			intercepted(c, apiURL, w, r)
		})
	}
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

func intercepted(c context.Context, apiURL string, w http.ResponseWriter, r *http.Request) {
	dlog.Debugf(c, "Received %s request", restapi.EndPontConsumeHere)

	apiRq, err := http.NewRequest("GET", apiURL+restapi.EndPontConsumeHere, nil)
	w.Header().Set("Content-Type", "text/plain")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		err = fmt.Errorf("failed to create http request: %w", err)
		if _, wErr := w.Write([]byte(err.Error())); wErr != nil {
			dlog.Errorf(c, "error %v while responding with error %v", wErr, err)
		}
		return
	}

	apiRq.Header = make(http.Header, len(r.Header)+1)
	apiRq.Header.Set(restapi.HeaderCallerInterceptID, os.Getenv("TELEPRESENCE_INTERCEPT_ID"))

	// The "X-With-" prefix is used as a backdoor to avoid triggering intercepts during test. It's
	// stripped off here.
	for h, v := range r.Header {
		apiRq.Header[strings.TrimPrefix(h, "X-With-")] = v
	}

	// The "X-Without-" prefix is used when the headers that trigger an intercept must be included in
	// order for the intercept to take place, but should be removed in the subsequent query. Both
	// the "X-Without-" headers and the header they refer to are stripped off here.
	for h := range apiRq.Header {
		if hw := strings.TrimPrefix(h, "X-Without-"); h != hw {
			apiRq.Header.Del(h)
			apiRq.Header.Del(hw)
		}
	}

	dlog.Debugf(c, "Sending request %s with headers %s", apiRq.URL, header.Stringer(apiRq.Header))
	rsp, err := http.DefaultClient.Do(apiRq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		err = fmt.Errorf("failed to execute http request: %w", err)
		if _, wErr := w.Write([]byte(err.Error())); wErr != nil {
			dlog.Errorf(c, "error %v while responding with error %v", wErr, err)
		}
		return
	}
	defer rsp.Body.Close()
	wh := w.Header()
	for h, v := range rsp.Header {
		wh[h] = v
	}
	w.WriteHeader(rsp.StatusCode)
	rl, err := io.Copy(w, rsp.Body)
	if err != nil {
		dlog.Errorf(c, "failed to copy message body: %v", err)
	}
	dlog.Debugf(c, "response len = %d", rl)
}
