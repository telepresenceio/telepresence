package auth

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/clog"
)

const (
	clientCAConfigMapNamespace = "kube-system"
	clientCAConfigMapName      = "extension-apiserver-authentication"
	clientCAConfigMapKey       = "client-ca-file"

	// clientCARefreshInterval is how often Start reloads the CA pool from the
	// ConfigMap in the background.
	clientCARefreshInterval = 10 * time.Minute

	// clientCAReloadCooldown rate-limits ReloadOnFailure so that a burst of
	// rejected connections triggers at most one extra API server read per
	// interval.
	clientCAReloadCooldown = 30 * time.Second

	// clientCAReloadTimeout bounds the ConfigMap GET a reload performs, so a stalled
	// API server can never pin mu -- and so a handshake blocked on ReloadOnFailure --
	// for longer than this, regardless of how long-lived the passed context is.
	// client-go's REST client honors context cancellation on the underlying HTTP
	// request, so a direct GET bounded by this timeout is sufficient on its own.
	clientCAReloadTimeout = 5 * time.Second
)

// caSnapshot is the CA pool together with the generation it was loaded at, swapped into
// ClientCAPool.snapshot atomically so a reader always sees a pool and its generation
// together.
type caSnapshot struct {
	pool       *x509.CertPool
	generation uint64
}

// ClientCAPool holds the cluster's client CA bundle, published by the API server in
// the client-ca-file key of the extension-apiserver-authentication ConfigMap in
// kube-system, that the x509 auth listener verifies presented certificates against. It
// refreshes on clientCARefreshInterval and, since CA rotation must not lock clients out
// for the length of that interval, on demand via ReloadOnFailure.
type ClientCAPool struct {
	client   kubernetes.Interface
	snapshot atomic.Pointer[caSnapshot]

	// mu serializes reload so that two overlapping reloads -- e.g. the periodic
	// refresh and an on-demand ReloadOnFailure -- can never apply their results out
	// of order and have an older read clobber a newer one.
	mu                sync.Mutex
	lastReloadAttempt time.Time
	bundleHash        [sha256.Size]byte
	bundleHashSet     bool
	generation        uint64
	onChange          func(generation uint64)
}

// NewClientCAPool creates a ClientCAPool and performs an initial load. A failure to
// load -- missing RBAC, an absent ConfigMap, or an absent key -- is logged and leaves
// the pool empty; verification against an empty pool fails cleanly until a reload
// succeeds.
func NewClientCAPool(ctx context.Context, client kubernetes.Interface) *ClientCAPool {
	p := &ClientCAPool{client: client}
	p.reload(ctx)
	return p
}

// Start runs the periodic refresh loop until ctx is done.
func (p *ClientCAPool) Start(ctx context.Context) {
	ticker := time.NewTicker(clientCARefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reload(ctx)
		}
	}
}

// Pool returns the current CA pool, or nil if no load has ever succeeded.
func (p *ClientCAPool) Pool() *x509.CertPool {
	if s := p.snapshot.Load(); s != nil {
		return s.pool
	}
	return nil
}

// Snapshot returns the current CA pool together with its generation. The generation
// increments on every content change and is 1 for the first successful load, 0 if no
// load has ever succeeded. The x509 listener records the generation it verified a
// certificate against and passes it to MintedTokens.Mint, which refuses to mint unless
// that generation still matches the store's current one -- see MintedTokens.
func (p *ClientCAPool) Snapshot() (*x509.CertPool, uint64) {
	if s := p.snapshot.Load(); s != nil {
		return s.pool, s.generation
	}
	return nil, 0
}

// OnChange registers f to be called, with the new generation, on every generation
// transition: the first successful load and every reload whose content differs from the
// one previously loaded. Not called for a reload that reloads the same content. Must be
// called before Start; only one callback may be registered.
func (p *ClientCAPool) OnChange(f func(generation uint64)) {
	p.mu.Lock()
	p.onChange = f
	p.mu.Unlock()
}

// ReloadOnFailure triggers an out-of-band reload following a failed verification, so
// that CA rotation doesn't lock out clients for the length of clientCARefreshInterval.
// Calls within clientCAReloadCooldown of the previous attempt are ignored.
func (p *ClientCAPool) ReloadOnFailure(ctx context.Context) {
	p.mu.Lock()
	if time.Since(p.lastReloadAttempt) < clientCAReloadCooldown {
		p.mu.Unlock()
		return
	}
	p.lastReloadAttempt = time.Now()
	p.mu.Unlock()
	p.reload(ctx)
}

// reload holds mu for the entire read-parse-store sequence, and on a generation
// transition invokes onChange before the new snapshot is stored. That order is
// fail-closed: a certificate can never be verified against one CA generation and
// minted against another.
func (p *ClientCAPool) reload(ctx context.Context) {
	reloadCtx, cancel := context.WithTimeout(ctx, clientCAReloadTimeout)
	defer cancel()

	p.mu.Lock()

	cm, err := p.client.CoreV1().ConfigMaps(clientCAConfigMapNamespace).Get(reloadCtx, clientCAConfigMapName, metav1.GetOptions{})
	if err != nil {
		p.mu.Unlock()
		clog.Errorf(ctx, "x509 auth: unable to read configmap %s/%s: %v", clientCAConfigMapNamespace, clientCAConfigMapName, err)
		return
	}
	pemData, ok := cm.Data[clientCAConfigMapKey]
	if !ok {
		p.mu.Unlock()
		clog.Errorf(ctx, "x509 auth: configmap %s/%s has no %q key", clientCAConfigMapNamespace, clientCAConfigMapName, clientCAConfigMapKey)
		return
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pemData)) {
		p.mu.Unlock()
		clog.Errorf(ctx, "x509 auth: %q key of configmap %s/%s contains no valid certificates", clientCAConfigMapKey, clientCAConfigMapNamespace, clientCAConfigMapName)
		return
	}

	hash := sha256.Sum256([]byte(pemData))
	changed := p.bundleHashSet && hash != p.bundleHash
	first := !p.bundleHashSet
	p.bundleHash = hash
	p.bundleHashSet = true
	if first || changed {
		p.generation++
		if onChange := p.onChange; onChange != nil {
			onChange(p.generation)
		}
	}
	p.snapshot.Store(&caSnapshot{pool: pool, generation: p.generation})
	p.mu.Unlock()
}
