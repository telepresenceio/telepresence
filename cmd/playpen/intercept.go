package main

import (
	"github.com/datawire/teleproxy/pkg/supervisor"
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
