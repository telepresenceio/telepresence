package fwd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
