package cli

import "fmt"

// ConnectOpt produces extra arguments for a `telepresence connect` invocation.
type ConnectOpt func() []string

// InterceptOpt produces extra arguments for a `telepresence intercept` or
// `telepresence ingest` invocation.
type InterceptOpt func() []string

// Port sets --port local:remote, where remote identifies the service port by
// name or number.
func Port(local int, remote string) InterceptOpt {
	return func() []string { return []string{"--port", fmt.Sprintf("%d:%s", local, remote)} }
}

// MountFalse sets --mount false, disabling filesystem mounting.
func MountFalse() InterceptOpt {
	return func() []string { return []string{"--mount", "false"} }
}

// HTTPHeader adds an --http-header key=value filter.
func HTTPHeader(k, v string) InterceptOpt {
	return func() []string { return []string{"--http-header", k + "=" + v} }
}

// HTTPPathPrefix adds an --http-path-prefix filter.
func HTTPPathPrefix(p string) InterceptOpt {
	return func() []string { return []string{"--http-path-prefix", p} }
}

// Replace sets --replace, so the traffic-agent replaces the application
// container instead of running alongside it.
func Replace() InterceptOpt {
	return func() []string { return []string{"--replace"} }
}

// WorkloadFlag sets --workload name.
func WorkloadFlag(name string) InterceptOpt {
	return func() []string { return []string{"--workload", name} }
}

// EnvFile sets --env-file path.
func EnvFile(path string) InterceptOpt {
	return func() []string { return []string{"--env-file", path} }
}
