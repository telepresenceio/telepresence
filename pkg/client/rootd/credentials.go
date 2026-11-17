package rootd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

var (
	errNoCredentialStream     = errors.New("the user daemon has no credential stream open to the root daemon; run 'telepresence connect' again")
	errCredentialStreamClosed = errors.New("the credential stream to the user daemon was closed")
	errCertificateExpired     = errors.New("the client certificate from the credential plugin has expired; run 'telepresence connect' again")
)

// credentialStreamWait bounds how long fetch waits for the user daemon to open a stream.
const credentialStreamWait = 10 * time.Second

// credentialBroker supplies cluster credentials without running anything. The first credential comes
// with the Connect request. Later ones are requested from the user daemon over the stream that it opens.
type credentialBroker struct {
	mu     sync.Mutex
	first  []byte
	stream *credentialStream
	nextID uint64

	// streamUp is closed and replaced each time serve installs a stream.
	streamUp   chan struct{}
	streamWait time.Duration
}

// credentialStream is one open ProvideCredentials stream. The pending map is guarded by the broker's mutex.
type credentialStream struct {
	srv     rpc.Daemon_ProvideCredentialsServer
	sendMu  sync.Mutex
	pending map[uint64]chan *rpc.CredentialReply
	done    chan struct{}
}

func newCredentialBroker(first []byte) *credentialBroker {
	return &credentialBroker{first: first, streamUp: make(chan struct{}), streamWait: credentialStreamWait}
}

// fetch returns the first credential once. After that it asks the user daemon.
func (b *credentialBroker) fetch(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	if first := b.first; first != nil {
		b.first = nil
		b.mu.Unlock()
		return first, nil
	}
	cs := b.stream
	if cs == nil {
		var err error
		if cs, err = b.awaitStream(ctx); err != nil {
			return nil, err
		}
	}
	b.nextID++
	id := b.nextID
	ch := make(chan *rpc.CredentialReply, 1)
	cs.pending[id] = ch
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(cs.pending, id)
		b.mu.Unlock()
	}()

	cs.sendMu.Lock()
	err := cs.srv.Send(&rpc.CredentialRequest{Id: id})
	cs.sendMu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return nil, errors.New(r.Error)
		}
		return r.ExecCredential, nil
	case <-cs.done:
		return nil, errCredentialStreamClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// awaitStream waits up to streamWait for a stream to be installed. It must be called with b.mu held
// and returns with b.mu held on success and released on failure.
func (b *credentialBroker) awaitStream(ctx context.Context) (*credentialStream, error) {
	timer := time.NewTimer(b.streamWait)
	defer timer.Stop()
	for b.stream == nil {
		up := b.streamUp
		b.mu.Unlock()
		select {
		case <-up:
		case <-timer.C:
			return nil, errNoCredentialStream
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		b.mu.Lock()
	}
	return b.stream, nil
}

// serve receives replies on srv until the stream or ctx ends. It replaces any stream that is already open.
func (b *credentialBroker) serve(ctx context.Context, srv rpc.Daemon_ProvideCredentialsServer) error {
	cs := &credentialStream{
		srv:     srv,
		pending: make(map[uint64]chan *rpc.CredentialReply),
		done:    make(chan struct{}),
	}
	b.mu.Lock()
	old := b.stream
	b.stream = cs
	close(b.streamUp)
	b.streamUp = make(chan struct{})
	b.mu.Unlock()
	if old != nil {
		close(old.done)
	}

	recvErr := make(chan error, 1)
	go func() {
		for {
			r, err := srv.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			b.deliver(ctx, cs, r)
		}
	}()

	var err error
	select {
	case <-ctx.Done():
	case <-srv.Context().Done():
	case err = <-recvErr:
	}
	b.mu.Lock()
	if b.stream == cs {
		b.stream = nil
		close(cs.done)
	}
	b.mu.Unlock()
	return err
}

func (b *credentialBroker) deliver(ctx context.Context, cs *credentialStream, r *rpc.CredentialReply) {
	b.mu.Lock()
	ch, ok := cs.pending[r.Id]
	delete(cs.pending, r.Id)
	b.mu.Unlock()
	if !ok {
		clog.Debugf(ctx, "ignoring credential reply %d that nobody waits for", r.Id)
		return
	}
	ch <- r
}

// waitCredentialExpiry returns an error when the client certificate expires, and nil when ctx ends first.
func waitCredentialExpiry(ctx context.Context, expiry time.Time) error {
	t := time.NewTimer(time.Until(expiry))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-t.C:
		return errCertificateExpired
	}
}
