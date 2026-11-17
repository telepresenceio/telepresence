package rootd

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// newCredentialFixture returns a guardFixture whose sessions have a credential broker.
func newCredentialFixture(t *testing.T, first []byte) (*guardFixture, *credentialBroker) {
	t.Helper()
	f := newGuardFixture(t)
	var broker *credentialBroker
	if first != nil {
		broker = newCredentialBroker(first)
	}
	f.svc.newSession = func(sessionCtx, _ context.Context, _ *rpc.NetworkConfig, _ chan<- time.Time, _ func(context.Context)) (*session, error) {
		return &session{
			Cluster:     &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: sessionCtx}},
			l4PortMap:   xsync.NewMap[types.AddrPortProto, uint16](),
			credentials: broker,
		}, nil
	}
	return f, broker
}

func TestCredentials_FirstOnceThenStream(t *testing.T) {
	f, broker := newCredentialFixture(t, []byte("first"))
	ctx := t.Context()
	_, err := f.connect(t, f.a)
	require.NoError(t, err)

	got, err := broker.fetch(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("first"), got)

	// The first credential is used once, and nothing is open yet.
	_, err = broker.fetch(ctx)
	require.ErrorIs(t, err, errNoCredentialStream)

	stream, err := f.a.ProvideCredentials(ctx)
	require.NoError(t, err)
	type result struct {
		cred []byte
		err  error
	}
	res := make(chan result, 1)
	go func() {
		// The stream may not be attached yet; retry until it is.
		for {
			cred, err := broker.fetch(ctx)
			if err != errNoCredentialStream {
				res <- result{cred, err}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	req, err := stream.Recv()
	require.NoError(t, err)
	require.NoError(t, stream.Send(&rpc.CredentialReply{Id: req.Id, ExecCredential: []byte("second")}))
	r := <-res
	require.NoError(t, r.err)
	require.Equal(t, []byte("second"), r.cred)

	// An error from the plugin reaches the caller.
	go func() {
		cred, err := broker.fetch(ctx)
		res <- result{cred, err}
	}()
	req, err = stream.Recv()
	require.NoError(t, err)
	require.NoError(t, stream.Send(&rpc.CredentialReply{Id: req.Id, Error: "plugin exploded"}))
	r = <-res
	require.ErrorContains(t, r.err, "plugin exploded")
}

func TestCredentials_OverlappingRequestsGetTheirOwnAnswers(t *testing.T) {
	f, broker := newCredentialFixture(t, []byte("first"))
	ctx := t.Context()
	_, err := f.connect(t, f.a)
	require.NoError(t, err)
	_, err = broker.fetch(ctx)
	require.NoError(t, err)

	stream, err := f.a.ProvideCredentials(ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		broker.mu.Lock()
		defer broker.mu.Unlock()
		return broker.stream != nil
	}, 5*time.Second, 5*time.Millisecond)

	const n = 5
	answers := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			cred, err := broker.fetch(ctx)
			if err != nil {
				answers[i] = err.Error()
				return
			}
			answers[i] = string(cred)
		})
	}
	reqs := make([]*rpc.CredentialRequest, n)
	for i := range n {
		reqs[i], err = stream.Recv()
		require.NoError(t, err)
	}
	// Answer in reverse order; each answer names the request it belongs to.
	for i := n - 1; i >= 0; i-- {
		require.NoError(t, stream.Send(&rpc.CredentialReply{Id: reqs[i].Id, ExecCredential: fmt.Appendf(nil, "for-%d", reqs[i].Id)}))
	}
	wg.Wait()

	ids := make(map[string]bool)
	for _, a := range answers {
		require.Regexp(t, `^for-\d+$`, a)
		ids[a] = true
	}
	require.Len(t, ids, n, "every caller must get a distinct answer: %v", answers)
}

func TestCredentials_StreamClosed(t *testing.T) {
	f, broker := newCredentialFixture(t, []byte("first"))
	_, err := f.connect(t, f.a)
	require.NoError(t, err)
	_, err = broker.fetch(t.Context())
	require.NoError(t, err)

	sctx, cancel := context.WithCancel(t.Context())
	stream, err := f.a.ProvideCredentials(sctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		broker.mu.Lock()
		defer broker.mu.Unlock()
		return broker.stream != nil
	}, 5*time.Second, 5*time.Millisecond)

	errCh := make(chan error, 1)
	go func() {
		_, err := broker.fetch(t.Context())
		errCh <- err
	}()
	_, err = stream.Recv()
	require.NoError(t, err)
	cancel()
	select {
	case err = <-errCh:
		require.ErrorIs(t, err, errCredentialStreamClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("a fetch must not wait for a stream that is gone")
	}

	broker.streamWait = time.Millisecond
	require.Eventually(t, func() bool {
		_, err := broker.fetch(t.Context())
		return err == errNoCredentialStream
	}, 5*time.Second, 5*time.Millisecond)
}

func TestCredentials_NoPluginNoStream(t *testing.T) {
	f, _ := newCredentialFixture(t, nil)
	_, err := f.connect(t, f.a)
	require.NoError(t, err)

	stream, err := f.a.ProvideCredentials(t.Context())
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
}

func TestCredentials_NoSession(t *testing.T) {
	f, _ := newCredentialFixture(t, []byte("first"))
	stream, err := f.a.ProvideCredentials(t.Context())
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
}

func TestWaitCredentialExpiry(t *testing.T) {
	t.Run("expiry ends the group with an error", func(t *testing.T) {
		g := log.NewGroup(t.Context())
		g.Go("credential-expiry", func(ctx context.Context) error {
			return waitCredentialExpiry(ctx, time.Now().Add(20*time.Millisecond))
		})
		// Another member of the group runs until the group is canceled.
		stopped := make(chan struct{})
		g.Go("other", func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return nil
		})
		err := g.Wait()
		require.ErrorIs(t, err, errCertificateExpired)
		assert.ErrorContains(t, err, "telepresence connect")
		<-stopped
	})

	t.Run("canceling the context is not an error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.NoError(t, waitCredentialExpiry(ctx, time.Now().Add(time.Hour)))
	})
}

func TestCredentials_FetchWaitsForStream(t *testing.T) {
	f, broker := newCredentialFixture(t, []byte("first"))
	_, err := f.connect(t, f.a)
	require.NoError(t, err)
	_, err = broker.fetch(t.Context())
	require.NoError(t, err)

	type result struct {
		cred []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		cred, err := broker.fetch(t.Context())
		resCh <- result{cred, err}
	}()
	time.Sleep(50 * time.Millisecond)

	stream, err := f.a.ProvideCredentials(t.Context())
	require.NoError(t, err)
	req, err := stream.Recv()
	require.NoError(t, err)
	require.NoError(t, stream.Send(&rpc.CredentialReply{Id: req.Id, ExecCredential: []byte("second")}))
	select {
	case r := <-resCh:
		require.NoError(t, r.err)
		require.Equal(t, []byte("second"), r.cred)
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not get the credential after the stream opened")
	}
}

func TestCredentials_FetchTimesOutWithoutStream(t *testing.T) {
	broker := newCredentialBroker(nil)
	broker.streamWait = 20 * time.Millisecond
	start := time.Now()
	_, err := broker.fetch(t.Context())
	require.ErrorIs(t, err, errNoCredentialStream)
	require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
}

func TestCredentials_FetchWaitEndsWithContext(t *testing.T) {
	broker := newCredentialBroker(nil)
	broker.streamWait = time.Minute
	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		_, err := broker.fetch(ctx)
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled context must end the wait")
	}
}
