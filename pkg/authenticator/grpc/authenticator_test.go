package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
)

type fakeAuthenticator struct{}

func (fakeAuthenticator) GetExecCredentials(context.Context, string) ([]byte, error) {
	return []byte("creds"), nil
}

func TestGetContextExecCredentials_OnlyServesItsContext(t *testing.T) {
	h := &AuthenticatorServer{authenticator: fakeAuthenticator{}, contextName: "mine"}

	res, err := h.GetContextExecCredentials(t.Context(), &rpc.GetContextExecCredentialsRequest{ContextName: "mine"})
	require.NoError(t, err)
	assert.Equal(t, "creds", string(res.RawCredentials))

	_, err = h.GetContextExecCredentials(t.Context(), &rpc.GetContextExecCredentialsRequest{ContextName: "other"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}
