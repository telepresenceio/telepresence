package quicforwarder

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestHealthHandler_ReadyReflectsAllowlist(t *testing.T) {
	allowlist := NewAllowlist(0)
	require.False(t, allowlist.Ready())

	handler := healthHandler(allowlist)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, 503, rec.Code)
	assert.Equal(t, "no backend allowlist snapshot received yet", rec.Body.String())

	managerIP := [4]byte{10, 1, 1, 1}
	allowlist.update(context.Background(), []*rpc.QuicBackend{{Ip: managerIP[:], Kind: "manager", Port: 7778}})
	require.True(t, allowlist.Ready())

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}
