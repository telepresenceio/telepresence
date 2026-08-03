package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// throughputBaseSize/throughputVariance size Test_LargeBodyRoundTrip's PUT
// body: a deterministic byte pattern of throughputBaseSize plus a
// pseudo-random amount under throughputVariance, so the exact size isn't a
// single fixed constant baked into both ends of the round trip.
const (
	throughputBaseSize = 12 * 1024 * 1024
	throughputVariance = 4096
)

// throughputTimeout bounds the PUT round trip: generous for a ~12MiB body
// through the tunnel and back.
const throughputTimeout = 60 * time.Second

// Throughput proves one bulk TCP round trip carries its full payload
// through the tunnel to an intercepted local handler. Connect/disconnect
// cycling is exercised by the framework's own fixture churn every run, and
// outbound proxying by every routing/dns assertion, so those old stress
// variants are not repeated here -- only the bulk-transfer axis they left
// uncovered.
//
// Labeled Slow: a ~12MiB round trip through the tunnel takes real
// wall-clock time.
type Throughput struct {
	rt.Suite
}

func init() {
	rt.Register(&Throughput{}, rt.InArea("session"), rt.NeedsManager(managers.Default), rt.WithLabels(rt.Slow))
}

// Test_LargeBodyRoundTrip intercepts an echo workload to a local handler
// (Suite.LocalEcho's rt.LocalService, whose PUT/POST path echoes the
// request body verbatim), PUTs a ~12MiB deterministic payload through the
// workload's Service URL, and requires the response body match it
// byte-for-byte.
func (s *Throughput) Test_LargeBodyRoundTrip() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("throughput"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	payload := throughputPayload(t)

	ctx, cancel := context.WithTimeout(s.Ctx(), throughputTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, wl.ServiceURL(), bytes.NewReader(payload))
	s.Require().NoError(err)
	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer resp.Body.Close()
	s.Require().Equal(http.StatusOK, resp.StatusCode)

	got, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)

	if !bytes.Equal(payload, got) {
		t.Fatalf("round-tripped body mismatch: sent %d bytes, got %d bytes back", len(payload), len(got))
	}
}

// throughputPayload returns a byte slice of throughputBaseSize plus a random
// amount under throughputVariance, filled with a repeating 0-255 ramp.
func throughputPayload(t testing.TB) []byte {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(throughputVariance))
	if err != nil {
		t.Fatalf("throughputPayload: %v", err)
	}
	size := throughputBaseSize + int(n.Int64())
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i)
	}
	return buf
}
