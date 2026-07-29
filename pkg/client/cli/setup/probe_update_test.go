package setup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func newUpdateServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeUpdate_NewerVersionAvailable(t *testing.T) {
	newer := version.Structured
	newer.Major++
	srv := newUpdateServer(t, newer.String()+"\n", http.StatusOK)

	p := &Prober{UpdateCheckHost: srv.URL}
	facts := p.probeUpdate(context.Background())

	require.Empty(t, facts.CheckError)
	assert.True(t, facts.UpdateAvailable)
	assert.Equal(t, newer.String(), facts.Latest)
}

func TestProbeUpdate_UpToDate(t *testing.T) {
	srv := newUpdateServer(t, version.Structured.String()+"\n", http.StatusOK)

	p := &Prober{UpdateCheckHost: srv.URL}
	facts := p.probeUpdate(context.Background())

	require.Empty(t, facts.CheckError)
	assert.False(t, facts.UpdateAvailable)
	assert.Empty(t, facts.Latest)
}

func TestProbeUpdate_NotFound(t *testing.T) {
	srv := newUpdateServer(t, "not found", http.StatusNotFound)

	p := &Prober{UpdateCheckHost: srv.URL}
	facts := p.probeUpdate(context.Background())

	assert.NotEmpty(t, facts.CheckError)
	assert.False(t, facts.UpdateAvailable)
}
