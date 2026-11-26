package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestNilNestedFieldsDoNotPanic(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

	sess := &rpc.SessionInfo{SessionId: "no-such-session"}
	tests := []struct {
		name string
		code codes.Code // codes.OK means any outcome except a panic
		call func() error
	}{
		{"ReconnectClient session only", codes.InvalidArgument, func() error {
			_, err := mgr.ReconnectClient(sctx, &rpc.ReconnectClientRequest{Session: sess})
			return err
		}},
		{"ReconnectClient client only", codes.OK, func() error {
			_, err := mgr.ReconnectClient(sctx, &rpc.ReconnectClientRequest{Client: &rpc.ClientInfo{}})
			return err
		}},
		{"ReconnectAgent session only", codes.InvalidArgument, func() error {
			_, err := mgr.ReconnectAgent(sctx, &rpc.ReconnectAgentRequest{Session: sess})
			return err
		}},
		{"ReconnectAgent agent only", codes.InvalidArgument, func() error {
			_, err := mgr.ReconnectAgent(sctx, &rpc.ReconnectAgentRequest{Agent: &rpc.AgentInfo{}})
			return err
		}},
		{"CreateIntercept session only", codes.InvalidArgument, func() error {
			_, err := mgr.CreateIntercept(sctx, &rpc.CreateInterceptRequest{Session: sess})
			return err
		}},
		{"PrepareIntercept session only", codes.InvalidArgument, func() error {
			_, err := mgr.PrepareIntercept(sctx, &rpc.CreateInterceptRequest{Session: sess})
			return err
		}},
		{"Lookup empty", codes.NotFound, func() error {
			_, err := mgr.Lookup(sctx, &rpc.LookupRequest{})
			return err
		}},
		{"EnsureAgent empty", codes.NotFound, func() error {
			_, err := mgr.EnsureAgent(sctx, &rpc.EnsureAgentRequest{})
			return err
		}},
		{"GetAgentConfig empty", codes.NotFound, func() error {
			_, err := mgr.GetAgentConfig(sctx, &rpc.AgentConfigRequest{})
			return err
		}},
		{"ResolveServicePort empty", codes.NotFound, func() error {
			_, err := mgr.ResolveServicePort(sctx, &rpc.ResolveServicePortRequest{})
			return err
		}},
		{"RemoveIntercept name only", codes.NotFound, func() error {
			_, err := mgr.RemoveIntercept(sctx, &rpc.RemoveInterceptRequest2{Name: "x"})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { err = tt.call() })
			if tt.code != codes.OK {
				require.Error(t, err)
				require.Equal(t, tt.code, status.Code(err), "%v", err)
			}
		})
	}
}
