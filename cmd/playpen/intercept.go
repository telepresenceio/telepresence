package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/pkg/errors"
)

// InterceptInfo tracks one intercept operation
type InterceptInfo struct {
	Name       string
	Deployment string
	Patterns   map[string]string
	TargetHost string
	TargetPort int
}

// Acquire an intercept from the traffic manager
func (cept *InterceptInfo) Acquire(p *supervisor.Process, tm *TrafficManager) (port int, err error) {
	reqPatterns := make([]map[string]string, len(cept.Patterns))
	for header, regex := range cept.Patterns {
		pattern := map[string]string{"name": header, "regex_match": regex}
		reqPatterns = append(reqPatterns, pattern)
	}
	request := map[string]interface{}{
		"name":     cept.Name,
		"patterns": reqPatterns,
	}
	reqData, err := json.Marshal(request)
	if err != nil {
		return
	}
	result, code, err := tm.request("POST", "intercept/"+cept.Name, reqData)
	if err != nil {
		err = errors.Wrap(err, "acquire intercept")
		return
	}
	if code == 404 {
		err = fmt.Errorf("Deployment %q is not known to the traffic manager", cept.Name)
		return
	}
	if !(200 <= code && code <= 299) {
		err = fmt.Errorf("acquire intercept: %s: %s", http.StatusText(code), result)
		return
	}
	port, err = strconv.Atoi(result)
	if err != nil {
		err = errors.Wrapf(err, "bad port number from traffic manager: %q", result)
		return
	}
	return
}

// Retain the given intercept. This likely needs to be called every
// five seconds or so.
func (cept *InterceptInfo) Retain(p *supervisor.Process, tm *TrafficManager, port int) error {
	data := []byte(fmt.Sprintf("{\"port\": %d}", port))
	result, code, err := tm.request("POST", "intercept/"+cept.Name, data)
	if err != nil {
		return errors.Wrap(err, "retain intercept")
	}
	if !(200 <= code && code <= 299) {
		return fmt.Errorf("retain intercept: %s: %s", http.StatusText(code), result)
	}
	return nil
}

// Release the given intercept.
func (cept *InterceptInfo) Release(p *supervisor.Process, tm *TrafficManager, port int) error {
	data := []byte(fmt.Sprintf("%d", port))
	result, code, err := tm.request("DELETE", "intercept/"+cept.Name, data)
	if err != nil {
		return errors.Wrap(err, "release intercept")
	}
	if !(200 <= code && code <= 299) {
		return fmt.Errorf("release intercept: %s: %s", http.StatusText(code), result)
	}
	return nil
}

// ListIntercepts lists active intercepts
func (d *Daemon) ListIntercepts(p *supervisor.Process, out *Emitter) error {
	for idx, intercept := range d.intercepts {
		out.Printf("%4d. %s\n", idx, intercept.Name)
		out.Printf("      Intercepting requests to %s when\n", intercept.Deployment)
		for k, v := range intercept.Patterns {
			out.Printf("      - %s: %s\n", k, v)
		}
		out.Printf("      and redirecting them to %s:%d\n", intercept.TargetHost, intercept.TargetPort)
	}
	if len(d.intercepts) == 0 {
		out.Println("No intercepts")
	}
	return nil
}

// AddIntercept adds one intercept
func (d *Daemon) AddIntercept(p *supervisor.Process, out *Emitter, intercept *InterceptInfo) error {
	for _, cept := range d.intercepts {
		if cept.Name == intercept.Name {
			out.Printf("Intercept with name %q already exists\n", intercept.Name)
			out.SendExit(1)
			return nil
		}
	}
	d.intercepts = append(d.intercepts, intercept)
	out.Printf("Added intercept %q\n", intercept.Name)
	return nil
}

// RemoveIntercept removes one intercept by name
func (d *Daemon) RemoveIntercept(p *supervisor.Process, out *Emitter, name string) error {
	for idx, intercept := range d.intercepts {
		if intercept.Name == name {
			d.intercepts = append(d.intercepts[:idx], d.intercepts[idx+1:]...)
			out.Printf("Removed intercept %q", name)
			return nil
		}
	}
	out.Printf("Intercept named %q not found\n", name)
	out.SendExit(1)
	return nil
}
