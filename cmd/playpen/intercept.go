package main

import (
	"fmt"
	"net/http"
	"strings"

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

// ListIntercepts lists active intercepts
func (d *Daemon) ListIntercepts(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	res := &strings.Builder{}
	for idx, intercept := range d.intercepts {
		fmt.Fprintf(res, "%4d. %s\n", idx, intercept.Name)
		fmt.Fprintf(res, "      Intercepting requests to %s when\n", intercept.Deployment)
		for k, v := range intercept.Patterns {
			fmt.Fprintf(res, "      - %s: %s\n", k, v)
		}
		fmt.Fprintf(res, "      and redirecting them to %s:%d\n", intercept.TargetHost, intercept.TargetPort)
	}
	if len(d.intercepts) == 0 {
		fmt.Fprintln(res, "No intercepts")
	}
	reply.Message = res.String()
	return nil
}

// AddIntercept adds one intercept
func (d *Daemon) AddIntercept(_ *http.Request, intercept *InterceptInfo, reply *StringReply) error {
	for _, cept := range d.intercepts {
		if cept.Name == intercept.Name {
			return errors.Errorf("Intercept with name %q already exists", intercept.Name)
		}
	}
	d.intercepts = append(d.intercepts, intercept)
	reply.Message = fmt.Sprintf("Added intercept %q", intercept.Name)
	return nil
}

// RemoveIntercept removes one intercept by name
func (d *Daemon) RemoveIntercept(_ *http.Request, request *StringArgs, reply *StringReply) error {
	name := request.Value
	for idx, intercept := range d.intercepts {
		if intercept.Name == name {
			d.intercepts = append(d.intercepts[:idx], d.intercepts[idx+1:]...)
			reply.Message = fmt.Sprintf("Removed intercept %q", name)
			return nil
		}
	}
	return errors.Errorf("Intercept named %q not found", name)
}
