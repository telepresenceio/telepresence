// Package network contains standalone performance experiments for the telepresence
// data path: how the client<->cluster tunnel and the client-side VIF netstack behave
// under adverse conditions (packet loss, latency, bulk load).
//
// Three experiments live here:
//
//   - Head-of-line blocking under loss (headofline_test.go): the QUIC tunnel's
//     per-flow streams vs. the gRPC tunnel's single shared byte stream.
//   - Datagram carriage for tunneled UDP (datagram_test.go): whether RFC 9221
//     datagram carriage helps an inner QUIC/HTTP-3 flow (a negative result).
//   - VIF bulk throughput (vifthroughput_test.go): whether the client-side
//     netstack's UDP endpoint buffers window-limit bulk UDP traffic. This is about
//     UDP in general, not QUIC -- every UDP flow that terminates at the VIF shares
//     those buffers, and the fix (if any) helps both tunnel transports equally.
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
package network
