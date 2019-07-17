package main

import (
	"fmt"
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

// Intercepts is the (temporary) list of intercepts
var Intercepts []*InterceptInfo

func listIntercepts() (string, error) {
	res := &strings.Builder{}
	for idx, intercept := range Intercepts {
		fmt.Fprintf(res, "%4d. %s\n", idx, intercept.Name)
		fmt.Fprintf(res, "      Intercepting requests to %s when\n", intercept.Deployment)
		for k, v := range intercept.Patterns {
			fmt.Fprintf(res, "      - %s: %s\n", k, v)
		}
		fmt.Fprintf(res, "      and redirecting them to %s:%d\n", intercept.TargetHost, intercept.TargetPort)
	}
	if len(Intercepts) == 0 {
		fmt.Fprintln(res, "No intercepts")
	}
	return res.String(), nil
}

func addIntercept(intercept *InterceptInfo) error {
	for _, cept := range Intercepts {
		if cept.Name == intercept.Name {
			return errors.Errorf("Intercept with name %q already exists", intercept.Name)
		}
	}
	Intercepts = append(Intercepts, intercept)
	return nil
}

func removeIntercept(name string) error {
	for idx, intercept := range Intercepts {
		if intercept.Name == name {
			Intercepts = append(Intercepts[:idx], Intercepts[idx+1:]...)
			return nil
		}
	}
	return errors.Errorf("Intercept named %q not found", name)
}
