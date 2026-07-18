//go:build perf

package network

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// run executes a command, streaming failures into the test log and failing the
// test on a non-zero exit. Used for setup/teardown steps that must succeed.
func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
}

// runQuiet executes a command discarding output and returning its error, for
// best-effort cleanup steps that should not fail a test.
func runQuiet(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func clientVersion(t *testing.T, telepresence string) string {
	t.Helper()
	out, err := exec.Command(telepresence, "version", "--output", "json").Output()
	if err != nil {
		t.Fatalf("telepresence version: %v", err)
	}
	// `version --output json` shapes vary across releases: some have a "client"
	// field, others wrap the human-readable table in "stdout". Scan only decoded
	// JSON string values -- in the raw bytes a `\n` is a two-character escape,
	// not whitespace, so a raw Fields scan would glue the next label onto the
	// version token.
	var v struct {
		Client string `json:"client"`
		Stdout string `json:"stdout"`
	}
	if json.Unmarshal(out, &v) == nil {
		if v.Client != "" {
			return v.Client
		}
		for _, tok := range strings.Fields(v.Stdout) {
			if strings.HasPrefix(tok, "v") && strings.Count(tok, ".") >= 2 {
				return tok
			}
		}
	}
	t.Fatalf("could not determine client version from: %s", out)
	return ""
}

// parseTunnelTransport extracts root_daemon.tunnel_transport from
// `telepresence status --output json`, or "" if absent.
func parseTunnelTransport(statusJSON []byte) string {
	var s struct {
		RootDaemon struct {
			TunnelTransport string `json:"tunnel_transport"`
		} `json:"root_daemon"`
	}
	if json.Unmarshal(statusJSON, &s) != nil {
		return ""
	}
	return s.RootDaemon.TunnelTransport
}

// timedGet performs one HTTP GET, draining the body, and reports how long the
// whole transfer took. The body is fully read so the timing reflects the
// complete download, which is what a stalled shared connection lengthens.
func timedGet(ctx context.Context, url string) streamResult {
	// A fresh client per request so the streams do not share an HTTP keep-alive
	// pool on the client side -- each is its own TCP connection into the tunnel,
	// which is what makes them independent flows to multiplex (or not).
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	return timedClientGet(ctx, client, url, 0)
}

// timedClientGet performs one GET on client -- of the first rangeBytes bytes when
// rangeBytes > 0, of the whole object otherwise -- draining the body and timing the
// complete exchange.
func timedClientGet(ctx context.Context, client *http.Client, url string, rangeBytes int) streamResult {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return streamResult{err: err}
	}
	if rangeBytes > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", rangeBytes-1))
	}
	resp, err := client.Do(req)
	if err != nil {
		return streamResult{err: err}
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return streamResult{err: err}
	}
	return streamResult{duration: time.Since(start)}
}
