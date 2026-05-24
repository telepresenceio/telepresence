package usg

import (
	"context"
	"time"

	"github.com/telepresenceio/clog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	usgrpc "github.com/telepresenceio/telepresence/rpc/v2/usg"
)

// SenderConfig configures the background sender. A zero FlushInterval defaults
// to 30 seconds.
type SenderConfig struct {
	Address       string
	Insecure      bool
	FlushInterval time.Duration
	BatchSize     int
}

// RunSender drains sink and ships pending reports to the configured collector.
// It returns only when ctx is cancelled. If the address is empty, the function
// drains the sink to discard any backlog but never dials a server.
//
// Per the design rules, all transient failures are logged at info level only;
// the function never returns an error to its caller and never surfaces a
// warning. Reports lost to a transient failure are re-enqueued and may be
// dropped if the sink is at cap.
func RunSender(ctx context.Context, sink Sink, cfg SenderConfig) {
	interval := cfg.FlushInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// Cap the batch size by both the local FIFO ceiling and the largest
	// batch a collector must accept. MaxQueueSize is currently far below
	// MaxBatchSize, so the latter is here for safety should the queue ever
	// be enlarged.
	batch := cfg.BatchSize
	if batch <= 0 || batch > MaxQueueSize {
		batch = MaxQueueSize
	}
	if batch > MaxBatchSize {
		batch = MaxBatchSize
	}

	var client usgrpc.UsgClient
	var conn *grpc.ClientConn
	if cfg.Address != "" {
		var err error
		conn, err = dialCollector(cfg.Address, cfg.Insecure)
		if err != nil {
			clog.Infof(ctx, "usg: collector unreachable, reports will be dropped: %v", err)
		} else {
			client = usgrpc.NewUsgClient(conn)
		}
	}

	t := time.NewTicker(interval)

	for {
		select {
		case <-ctx.Done():
			// Final flush. ctx is already cancelled, so flush(ctx, …)
			// would short-circuit on its WithTimeout(ctx, …); detach it
			// so the in-flight reports actually get a chance to ship.
			flush(context.WithoutCancel(ctx), sink, client, batch)
			t.Stop()
			if conn != nil {
				_ = conn.Close()
			}
			return
		case <-t.C:
			flush(ctx, sink, client, batch)
		}
	}
}

func flush(ctx context.Context, sink Sink, client usgrpc.UsgClient, batch int) {
	reports := sink.Drain(batch)
	if len(reports) == 0 {
		return
	}
	if client == nil {
		// No collector configured: discard.
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := client.ReportBatch(sendCtx, &usgrpc.UsageReportBatch{Reports: reports})
	if err != nil {
		clog.Infof(ctx, "usg: batch of %d reports could not be sent: %v", len(reports), err)
		// Re-enqueue best-effort; overflow drops silently as required.
		for _, r := range reports {
			sink.Enqueue(r)
		}
	}
}

func dialCollector(addr string, allowInsecure bool) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if allowInsecure {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(nil)
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
}
