// Package check provides polling HTTP assertion helpers.
package check

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// pollInterval is how often EventuallyHTTP retries the request.
const pollInterval = 250 * time.Millisecond

// ReqOpt mutates the probe request before it is sent.
type ReqOpt func(*http.Request)

// WithHeader returns a ReqOpt that sets header k to v on each probe.
func WithHeader(k, v string) ReqOpt {
	return func(r *http.Request) {
		r.Header.Set(k, v)
	}
}

// EventuallyHTTP polls url until want returns true for the response's status
// and body, or fails t once timeout elapses. Every probe uses a fresh TCP
// connection: route-flip assertions (intercept attach/detach) would otherwise
// observe a stale keep-alive connection whose end-to-end pipe outlives the
// route change.
func EventuallyHTTP(t testing.TB, url string, want func(status int, body string) bool, timeout time.Duration, opts ...ReqOpt) {
	t.Helper()
	client := &http.Client{
		Timeout:   pollInterval * 4,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastStatus int
	var lastBody string
	for {
		status, body, err := probe(client, url, opts)
		lastErr = err
		if err == nil {
			lastStatus, lastBody = status, body
			if want(status, body) {
				return
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}
	if lastErr != nil {
		t.Fatalf("EventuallyHTTP %s: timed out after %s: %v", url, timeout, lastErr)
	}
	t.Fatalf("EventuallyHTTP %s: timed out after %s: last status %d, body %q", url, timeout, lastStatus, lastBody)
}

func probe(client *http.Client, url string, opts []ReqOpt) (int, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(body), nil
}

// BodyContains returns an EventuallyHTTP predicate that accepts a 200
// response whose body contains s.
func BodyContains(s string) func(int, string) bool {
	return func(status int, body string) bool {
		return status == http.StatusOK && strings.Contains(body, s)
	}
}

// EventuallyFile polls path until it is readable and want(content) returns
// true, or fails t once timeout elapses. A FUSE/SFTP intercept mount's
// content appears asynchronously (the mount is wired up after the intercept
// itself is confirmed, and the mounted directory may not exist yet), so a
// single read right after Conn.Intercept/Ingest can race it.
func EventuallyFile(t testing.TB, path string, want func([]byte) bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastContent []byte
	for {
		content, err := os.ReadFile(path)
		lastErr = err
		if err == nil {
			lastContent = content
			if want(content) {
				return
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}
	if lastErr != nil {
		t.Fatalf("EventuallyFile %s: timed out after %s: %v", path, timeout, lastErr)
	}
	t.Fatalf("EventuallyFile %s: timed out after %s: content %q did not match", path, timeout, lastContent)
}
