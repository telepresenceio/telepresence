package main

// InterceptInfo tracks one intercept operation
type InterceptInfo struct {
	Name       string
	Deployment string
	Patterns   map[string]string
	TargetHost string
	TargetPort int
}
