package itest

import (
	"context"
	"encoding/json"

	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cmd"
)

type StatusResponse struct {
	RootDaemon cmd.RootDaemonStatus `json:"root_daemon,omitempty"`
	UserDaemon cmd.UserDaemonStatus `json:"user_daemon,omitempty"`
}

func TelepresenceStatus(ctx context.Context, args ...string) (*StatusResponse, error) {
	stdout, stderr, err := Telepresence(ctx, append([]string{"status", "--output", "json"}, args...)...)
	if err != nil {
		dlog.Error(ctx, stderr)
		return nil, err
	}
	var status StatusResponse
	err = json.Unmarshal([]byte(stdout), &status)
	if err != nil {
		return nil, err
	}
	return &status, nil
}

func TelepresenceStatusOk(ctx context.Context, args ...string) *StatusResponse {
	status, err := TelepresenceStatus(ctx, args...)
	require.NoError(getT(ctx), err)
	return status
}
