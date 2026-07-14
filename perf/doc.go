// Package perf contains standalone performance experiments that quantify the
// benefit of the QUIC tunnel transport (docs/reference/quic-transport-architecture.md)
// against the default port-forwarded gRPC transport.
//
// The experiments are NOT part of `make check-integration` or `go test ./...`:
// every file that carries an experiment is behind the `perf` build tag, so a
// normal build or vet skips them. Run them explicitly, on a cluster, with:
//
//	make perf                 # or: go test -tags perf -timeout 30m ./perf/...
//
// See perf/README.md for prerequisites (a cluster, a QUIC-reachable UDP path,
// passwordless sudo for `tc`) and for how to read the results. This file has no
// build tag purely so that `go build ./...` sees a non-empty package.
package perf
