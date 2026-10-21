package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
)

type pendingWorkloadSession struct {
	userd.Session
	ctx     context.Context
	waiting chan struct{}
}

func (s *pendingWorkloadSession) Done() <-chan struct{} { return s.ctx.Done() }
func (s *pendingWorkloadSession) Err() error            { return s.ctx.Err() }
func (s *pendingWorkloadSession) MarkActivity()         {}

func (s *pendingWorkloadSession) WorkloadInfoSnapshot(ctx context.Context, _ []string, _ rpc.ListRequest_Filter) (*rpc.WorkloadInfoSnapshot, error) {
	close(s.waiting)
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func TestListCancellationReleasesSessionLock(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(t.Context())
	defer cancelSession()
	sess := &pendingWorkloadSession{ctx: sessionCtx, waiting: make(chan struct{})}
	svc := &service{session: sess}
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	defer cancelRequest()
	result := make(chan error, 1)
	go func() {
		_, err := svc.List(requestCtx, &rpc.ListRequest{Namespace: "app-space"})
		result <- err
	}()
	select {
	case <-sess.waiting:
	case <-time.After(time.Second):
		t.Fatal("List did not reach the workload snapshot")
	}
	cancelRequest()
	select {
	case err := <-result:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("List did not honor request cancellation")
	}
	require.NoError(t, sessionCtx.Err(), "canceling List must not cancel the session")
	require.True(t, svc.sessionLock.TryLock(), "a canceled List must release the session for reconnect or quit")
	svc.sessionLock.Unlock()
}
