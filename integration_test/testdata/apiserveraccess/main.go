package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

// This service is meant for testing the cluster side Telepresence API service.
//
// Publish image to cluster:
//
//	ko publish -B ./integration_test/testdata/apiserveraccess [--insecure-registry]
//
// Deploy it:
//
//	kubectl apply -f ./k8s/apitest.yaml
//
// Run it locally using an intercept with -- so that TELEPRESENCE_INTERCEPT_ID is propagated in the env
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

	ln, err := net.Listen("tcp", "localhost:"+ap)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if apiUrl, err := apiURL(); err == nil {
		consumeHereURL := apiUrl + "/consume-here"
		interceptInfoURL := apiUrl + "/intercept-info"
		mux.HandleFunc("/consume-here", func(w http.ResponseWriter, r *http.Request) {
			var b bool
			intercepted(c, consumeHereURL, r.FormValue("path"), w, r, &b)
		})
		mux.HandleFunc("/intercept-info", func(w http.ResponseWriter, r *http.Request) {
			ii := restapi.InterceptInfo{}
			intercepted(c, interceptInfoURL, r.FormValue("path"), w, r, &ii)
		})
	} else {
		mux.HandleFunc("/consume-here", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(false)
		})
		mux.HandleFunc("/intercept-info", func(w http.ResponseWriter, r *http.Request) {
			ii := restapi.InterceptInfo{}
			_ = json.NewEncoder(w).Encode(&ii)
		})
	}

	server := &dhttp.ServerConfig{Handler: mux}
	info := fmt.Sprintf("API test server on %v", ln.Addr())
	dlog.Infof(c, "%s started", info)
	defer dlog.Infof(c, "%s ended", info)
	if err := server.Serve(c, ln); err != nil && err != c.Err() {
		return fmt.Errorf("%s stopped: %w", info, err)
	}
	return nil
}

const interceptIdEnv = "TELEPRESENCE_INTERCEPT_ID"

// apiURL creates the generic URL needed to access the service
func apiURL() (string, error) {
	pe := os.Getenv(agentconfig.EnvAPIPort)
	if _, err := strconv.ParseUint(pe, 10, 16); err != nil {
		return "", fmt.Errorf("value %q of env %s does not represent a valid port number", pe, agentconfig.EnvAPIPort)
	}
	return "http://localhost:" + pe, nil
}

// doRequest calls the consume-here endpoint with the given headers and returns the result
func doRequest(c context.Context, rqUrl string, path string, hm map[string]string, objTemplate any, er *restapi.ErrorResponse) (int, error) {
	rq, err := http.NewRequest("GET", rqUrl+"?path="+url.QueryEscape(path), nil)
	if err != nil {
		return 0, err
	}
	rq.Header = make(http.Header, len(hm)+1)
	rq.Header.Set("X-Telepresence-Caller-Intercept-Id", os.Getenv(interceptIdEnv))
	for k, v := range hm {
		rq.Header.Set(k, v)
	}
	dlog.Debugf(c, "%s with headers\n%s", rqUrl, matcher.HeaderStringer(rq.Header))
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		return 0, err
	}
	defer rs.Body.Close()

	ec := json.NewDecoder(rs.Body)
	if rs.StatusCode == http.StatusOK {
		err = ec.Decode(objTemplate)
	} else {
		// Make an attempt to decode a json error.
		_ = ec.Decode(er)
	}
	return rs.StatusCode, err
}

func intercepted(c context.Context, url string, path string, w http.ResponseWriter, r *http.Request, objTemplate any) {
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
	w.Header().Set("Content-Type", "application/json")

	er := restapi.ErrorResponse{}
	if status, err := doRequest(c, url, path, hm, objTemplate, &er); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to execute http request: %v", err)
	} else {
		w.WriteHeader(status)
		if status == http.StatusOK {
			err = json.NewEncoder(w).Encode(objTemplate)
		} else if er.Error != "" {
			err = json.NewEncoder(w).Encode(er)
		}
		if err != nil {
			dlog.Error(c, err)
		}
	}
}
