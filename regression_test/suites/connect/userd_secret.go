package connect

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// UserDaemonSecret proves that the host user daemon serves only callers that
// present the secret in its info file. Version is the one open call.
type UserDaemonSecret struct {
	rt.Suite
}

func init() {
	rt.Register(&UserDaemonSecret{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
	)
}

// userDaemonInfo reads the host user daemon's info file without going through
// the loader, which deletes a file it considers stale.
func (s *UserDaemonSecret) userDaemonInfo() *daemon.Info {
	t := s.T()
	path := filepath.Join(rt.R().UserCacheDir(), "userd", daemon.InfoFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("daemon info file %s: %v", path, err)
	}
	var info daemon.Info
	if err = json.Unmarshal(data, &info); err != nil {
		t.Fatalf("daemon info file %s: %v", path, err)
	}
	return &info
}

func (s *UserDaemonSecret) dial(info *daemon.Info) connector.ConnectorClient {
	t := s.T()
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", info.DaemonPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
	if err != nil {
		t.Fatalf("dial user daemon on port %d: %v", info.DaemonPort, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return connector.NewConnectorClient(conn)
}

func (s *UserDaemonSecret) Test_CallsNeedTheSecret() {
	t := s.T()
	s.Connect()
	info := s.userDaemonInfo()
	s.Require().NotZero(info.DaemonPort, "the info file must name the user daemon's port")
	s.Require().NotEmpty(info.Token, "the info file must hold the user daemon's secret")
	uc := s.dial(info)

	ctx, cancel := context.WithTimeout(s.Ctx(), 30*time.Second)
	defer cancel()
	withToken := func(token string) context.Context {
		return metadata.AppendToOutgoingContext(ctx, server.TokenMetadataKey, token)
	}

	// Version answers without a secret, so that a CLI of another version still gets the version-mismatch message.
	vi, err := uc.Version(ctx, &emptypb.Empty{})
	s.Require().NoError(err, "Version without a secret")
	s.NotEmpty(vi.GetVersion())

	// Every other call is refused, whether it carries no secret or a wrong one.
	_, err = uc.Status(ctx, &emptypb.Empty{})
	s.Equal(codes.Unauthenticated, status.Code(err), "Status without a secret: %v", err)
	_, err = uc.Status(withToken("not-the-secret"), &emptypb.Empty{})
	s.Equal(codes.Unauthenticated, status.Code(err), "Status with a wrong secret: %v", err)

	ws, err := uc.WatchWorkloads(ctx, &connector.WatchWorkloadsRequest{})
	s.Require().NoError(err)
	_, err = ws.Recv()
	s.Equal(codes.Unauthenticated, status.Code(err), "WatchWorkloads without a secret: %v", err)

	ws, err = uc.WatchWorkloads(withToken("not-the-secret"), &connector.WatchWorkloadsRequest{})
	s.Require().NoError(err)
	_, err = ws.Recv()
	s.Equal(codes.Unauthenticated, status.Code(err), "WatchWorkloads with a wrong secret: %v", err)

	// The secret from the info file opens the door.
	_, err = uc.Status(withToken(info.Token), &emptypb.Empty{})
	s.NoError(err, "Status with the secret from the info file")
	t.Logf("user daemon on port %d refused calls without its secret", info.DaemonPort)
}
