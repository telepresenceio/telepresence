package usg

import (
	"context"
	"path/filepath"

	"github.com/telepresenceio/clog"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// InstallClient prepares the client-side usage producer and returns a context
// with the producer attached when reporting is enabled, along with the disk
// sink the producer is writing to. The CLI ignores the returned sink; the
// user daemon passes it to RunSender so the producer and the sender share a
// single DiskSink instance (and its mutex) against the same on-disk FIFO.
// When reporting is disabled (or installation id / disk queue cannot be
// obtained), the original context is returned with a nil sink — no producer
// is attached.
//
// The sink is the on-disk FIFO at "{cacheDir}/usage-pending/". The caller is
// expected to start a sender goroutine in the user daemon (see RunSender);
// the CLI itself does not run a sender — it only enqueues and exits.
func InstallClient(ctx context.Context) (context.Context, Sink) {
	cfg := client.GetConfig(ctx).Usage()
	if cfg == nil || !cfg.Enabled {
		return ctx, nil
	}
	id, err := client.InstallID(ctx)
	if err != nil {
		clog.Infof(ctx, "usg: installation id unavailable, reporting disabled: %v", err)
		return ctx, nil
	}
	sink, err := NewDiskSink(ClientQueueDir(ctx))
	if err != nil {
		clog.Infof(ctx, "usg: disk queue unavailable, reporting disabled: %v", err)
		return ctx, nil
	}
	return WithProducer(ctx, newProducer(SourceClient, id, version.Version, sink)), sink
}

// ClientQueueDir is the on-disk directory where pending client reports live.
// Exposed so the user daemon's sender goroutine can open the same FIFO.
func ClientQueueDir(ctx context.Context) string {
	return filepath.Join(filelocation.AppUserCacheDir(ctx), "usage-pending")
}

// InstallManager prepares the manager-side usage producer with an in-memory
// FIFO and returns the ctx with the producer attached plus the sink the
// caller should pass to RunSender. Only call this when reporting is enabled
// — there is no disabled path.
func InstallManager(ctx context.Context, installID string) (context.Context, Sink) {
	sink := NewMemSink()
	return WithProducer(ctx, newProducer(SourceManager, installID, version.Version, sink)), sink
}
