//go:build perf

package network

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// h3PayloadURL is the fixed asset served by the perf-h3 workload (testdata/h3server.yaml),
// sized by that manifest's PAYLOAD_BYTES env var; every request in this experiment reads
// only the first datagramRequestBytes of it via a Range header, so the full size only bounds how
// large a request the workload could serve, not how much any single request transfers.
const h3PayloadURL = "https://perf-h3/payload.bin"

// newH3Client returns an *http.Client backed by a single http3.Transport, and a close func to
// release it once the caller is done. Certificate verification is skipped: the in-cluster h3
// server (testdata/h3server) presents a self-signed certificate with no cluster CA behind it,
// and this experiment is about tunnel transport behavior, not certificate trust.
func newH3Client() (*http.Client, func()) {
	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return &http.Client{Transport: tr}, func() { _ = tr.Close() }
}

// warmupH3 is config.warmup's HTTP/3 counterpart: it drives the h3 payload path until it
// flows -- a fresh session's DNS and tunnel may lag connect, and the inner QUIC handshake
// itself costs a round trip -- then runs a short full-concurrency burst on client whose
// timings are discarded. Without this, session establishment and the inner handshake would
// land in the first measured window and make it incomparable to the later ones.
func (c config) warmupH3(t *testing.T, client *http.Client, workers, reqBytes int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r := timedClientGet(ctx, client, h3PayloadURL, 0)
		cancel()
		if r.err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("warmup: h3 payload never became reachable through the tunnel: %v", r.err)
		}
		t.Logf("warmup: %v; retrying", r.err)
		time.Sleep(2 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	results := runH3Workers(ctx, client, h3PayloadURL, workers, reqBytes, 0, 5*time.Second)
	if p := summarize(results); p.failures > 0 {
		t.Logf("warmup window: %d of %d requests failed", p.failures, len(results))
		logFailures(t, results)
	}
}

// runH3Workers is runRequestWorkers' HTTP/3 counterpart, with one deliberate difference:
// every worker issues its Range GETs through the SAME client -- and therefore the same inner
// QUIC connection to url's host -- instead of getting its own. That shared connection is the
// point: concurrent HTTP/3 requests over it become concurrent streams multiplexed onto the
// SAME tunneled UDP flow (one client-side source port for the whole inner connection, hence
// one ConnID; see pkg/tunnel/datagram.go), so a single lost carrier packet on the tunnel
// either stalls every worker (stream carriage keeps that flow's Normal messages in one
// ordered sequence) or only the one inner UDP packet it happened to carry (datagram
// carriage). runRequestWorkers gives every worker its OWN client for the opposite reason:
// the head-of-line experiment measures independently-tunneled flows, one per worker.
func runH3Workers(ctx context.Context, client *http.Client, url string, workers, reqBytes int, think, dur time.Duration) []streamResult {
	deadline := time.Now().Add(dur)
	resCh := make(chan streamResult, 1024)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				r := timedClientGet(reqCtx, client, url, reqBytes)
				cancel()
				select {
				case resCh <- r:
				case <-ctx.Done():
					return
				}
				if ctx.Err() != nil {
					return
				}
				if think > 0 {
					select {
					case <-time.After(think):
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() { wg.Wait(); close(resCh) }()
	var results []streamResult
	for r := range resCh {
		results = append(results, r)
	}
	return results
}
