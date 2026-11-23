package fwd

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAddRequestTaps_MultipleTaps_ReceiveHeaderAndBody is a regression test for two bugs in
// addRequestTaps: it used to fail request.Write's Content-Length validation for any request
// with a positive, known body length, and its per-tap goroutines raced on a shared "err"
// variable instead of each using its own.
func TestAddRequestTaps_MultipleTaps_ReceiveHeaderAndBody(t *testing.T) {
	const body = "the quick brown fox"
	req := httptest.NewRequest(http.MethodPost, "http://example.com/path?q=1", strings.NewReader(body))
	req.Header.Set("X-Test", "value")

	const tapCount = 3
	taps, tapReader, err := addRequestTaps(context.Background(), req, tapCount, wiretapCacheSize)
	require.NoError(t, err)
	require.Len(t, taps, tapCount)
	defer tapReader.closeTaps()

	// The teed body must still be fully readable by the real consumer (the reverse proxy
	// or the intercepting client).
	got, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(got))

	wg := sync.WaitGroup{}
	wg.Add(tapCount)
	for i, tap := range taps {
		go func(i int, tap io.Reader) {
			defer wg.Done()
			b, err := io.ReadAll(tap)
			require.NoErrorf(t, err, "tap %d", i)
			s := string(b)
			require.Containsf(t, s, "POST /path?q=1", "tap %d missing request line", i)
			require.Containsf(t, s, "X-Test: value", "tap %d missing header", i)
			require.Containsf(t, s, body, "tap %d missing body", i)
		}(i, tap)
	}
	wg.Wait()
}

// TestAddRequestTaps_ZeroCount verifies the no-op path.
func TestAddRequestTaps_ZeroCount(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/path", nil)
	taps, tapReader, err := addRequestTaps(context.Background(), req, 0, wiretapCacheSize)
	require.NoError(t, err)
	require.Nil(t, taps)
	require.Nil(t, tapReader)
}

// TestAddRequestTaps_HugeContentLength verifies that a huge declared Content-Length neither
// delays the call nor changes the captured header.
func TestAddRequestTaps_HugeContentLength(t *testing.T) {
	const declared = int64(1) << 60
	req := httptest.NewRequest(http.MethodPost, "http://example.com/path", strings.NewReader("abc"))
	req.ContentLength = declared
	req.Header.Set("X-Test", "value")

	start := time.Now()
	taps, tapReader, err := addRequestTaps(context.Background(), req, 1, 16)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
	tapReader.closeTaps()

	b, err := io.ReadAll(taps[0])
	require.NoError(t, err)
	require.Contains(t, string(b), "\r\nContent-Length: 1152921504606846976\r\n")
	require.Contains(t, string(b), "X-Test: value")
}

// TestAddRequestTaps_HeaderMatchesWrite verifies that the captured header equals what
// request.Write produces for an ordinary request.
func TestAddRequestTaps_HeaderMatchesWrite(t *testing.T) {
	const body = "the quick brown fox"
	mk := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://example.com/path?q=1", strings.NewReader(body))
		r.Header.Set("X-Test", "value")
		return r
	}
	var want bytes.Buffer
	require.NoError(t, mk().Write(&want))
	wantHeader := want.String()[:strings.Index(want.String(), "\r\n\r\n")+4]

	taps, tapReader, err := addRequestTaps(context.Background(), mk(), 1, 16)
	require.NoError(t, err)
	tapReader.closeTaps()
	got, err := io.ReadAll(taps[0])
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(got), wantHeader), "got %q want prefix %q", got, wantHeader)
}
