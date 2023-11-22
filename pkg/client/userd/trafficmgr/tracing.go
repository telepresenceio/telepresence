package trafficmgr

import (
	"compress/gzip"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
	core "k8s.io/api/core/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type traceCollector struct {
	*connector.TracesRequest
}

func (*traceCollector) tracesFor(ctx context.Context, conn *grpc.ClientConn, ch chan<- []byte, component string) error {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "tracesFor", trace.WithAttributes(attribute.String("component", component)))
	defer span.End()
	cli := common.NewTracingClient(conn)
	cfg := client.GetConfig(ctx)
	maxRecSize := int64(1024 * 1024 * 20) // Default to 20 Mb here. There might be a lot of traces.
	if mz := cfg.Grpc().MaxReceiveSize(); mz > maxRecSize {
		maxRecSize = mz
	}
	result, err := cli.DumpTraces(ctx, &emptypb.Empty{}, grpc.MaxCallRecvMsgSize(int(maxRecSize)))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	data := result.GetTraceData()
	select {
	case ch <- data:
	case <-ctx.Done():
	}
	return nil
}

func (*traceCollector) launchTraceWriter(ctx context.Context, destFile string) (chan<- []byte, <-chan error, error) {
	ch := make(chan []byte)
	var err error
	if destFile, err = filepath.Abs(destFile); err != nil {
		return nil, nil, err
	}
	file, err := os.Create(destFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create trace file: %w", err)
	}
	errCh := make(chan error)

	go func() {
		zipW := gzip.NewWriter(file)
		defer func() {
			err = zipW.Close()
			if err != nil {
				errCh <- err
				return
			}
			err = file.Close()
			if err != nil {
				errCh <- err
				return
			}
			close(errCh)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case data, ok := <-ch:
				if !ok {
					return
				}
				_, err := zipW.Write(data)
				if err != nil {
					errCh <- err
					return
				}
			}
		}
	}()
	return ch, errCh, nil
}

func (c *traceCollector) userdTraces(ctx context.Context, tCh chan<- []byte) error {
	userdConn, err := socket.Dial(ctx, socket.UserDaemonPath(ctx), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	if err != nil {
		return err
	}
	defer userdConn.Close()

	return c.tracesFor(ctx, userdConn, tCh, "user-daemon")
}

func (c *traceCollector) rootdTraces(ctx context.Context, tCh chan<- []byte) error {
	dConn, err := socket.Dial(ctx, socket.RootDaemonPath(ctx), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	if err != nil {
		return err
	}
	defer dConn.Close()

	return c.tracesFor(ctx, dConn, tCh, "root-daemon")
}

func (c *traceCollector) trafficManagerTraces(ctx context.Context, sess *session, tCh chan<- []byte, remotePort string) error {
	span := trace.SpanFromContext(ctx)
	host := "svc/traffic-manager." + sess.GetManagerNamespace()
	grpcAddr := net.JoinHostPort(host, remotePort)
	span.SetAttributes(attribute.String("traffic-manager.host", host), attribute.String("traffic-manager.port", remotePort))
	tc, tCancel := context.WithTimeout(ctx, 20*time.Second)
	defer tCancel()

	opts := []grpc.DialOption{
		grpc.WithContextDialer(sess.pfDialer.Dial),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}

	conn, err := grpc.DialContext(tc, grpcAddr, opts...)
	if err != nil {
		return err
	}
	return c.tracesFor(ctx, conn, tCh, "traffic-manager")
}

func (c *traceCollector) agentTraces(ctx context.Context, sess *session, tCh chan<- []byte, remotePort string) error {
	return sess.ForeachAgentPod(ctx, func(ctx context.Context, pi typed.PodInterface, pod *core.Pod) {
		span := trace.SpanFromContext(ctx)
		name := fmt.Sprintf("%s.%s", pod.Name, pod.Namespace)
		addr := net.JoinHostPort(name, remotePort)
		tc, tCancel := context.WithTimeout(ctx, 20*time.Second)
		defer tCancel()

		opts := []grpc.DialOption{
			grpc.WithContextDialer(sess.pfDialer.Dial),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithNoProxy(),
			grpc.WithBlock(),
			grpc.WithReturnConnectionError(),
			grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		}

		conn, err := grpc.DialContext(tc, addr, opts...)
		if err != nil {
			err := fmt.Errorf("error getting traffic-agent traces for %s: %v", name, err)
			span.RecordError(err, trace.WithAttributes(
				attribute.String("host", name),
				attribute.String("port", remotePort),
			))
			dlog.Error(ctx, err)
			return
		}
		defer conn.Close()
		err = c.tracesFor(tc, conn, tCh, "traffic-agent")
		if err != nil {
			err := fmt.Errorf("error getting traffic-agent traces for %s: %v", name, err)
			span.RecordError(err, trace.WithAttributes(
				attribute.String("traffic-agent.host", name),
				attribute.String("traffic-agent.port", remotePort),
			))
			dlog.Error(ctx, err)
			return
		}
	}, nil)
}

func (s *session) GatherTraces(ctx context.Context, tr *connector.TracesRequest) *common.Result {
	return errcat.ToResult((&traceCollector{tr}).gatherTraces(ctx, s))
}

func (c *traceCollector) gatherTraces(ctx context.Context, sess *session) error {
	// Since we want this trace to show up in the gather traces output file, we'll declare it as a root trace and end it right after awaiting the wait group
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "gather-traces", trace.WithNewRoot())
	port := strconv.FormatUint(uint64(c.RemotePort), 10)

	tCh, errCh, err := c.launchTraceWriter(ctx, c.TracingFile)
	if err != nil {
		return err
	}

	wg := &sync.WaitGroup{}
	wg.Add(3)

	go func() {
		defer wg.Done()
		err := c.rootdTraces(ctx, tCh)
		if err != nil {
			err := fmt.Errorf("failed to collect root daemon traces: %v", err)
			span.RecordError(err)
			dlog.Error(ctx, err)
		}
	}()

	go func() {
		defer wg.Done()
		err = c.trafficManagerTraces(ctx, sess, tCh, port)
		if err != nil {
			err := fmt.Errorf("failed to collect traffic-manager traces: %v", err)
			span.RecordError(err)
			dlog.Error(ctx, err)
		}
	}()

	go func() {
		defer wg.Done()
		err := c.agentTraces(ctx, sess, tCh, port)
		if err != nil {
			err := fmt.Errorf("failed to collect traffic agent traces: %v", err)
			span.RecordError(err)
			dlog.Error(ctx, err)
		}
	}()

	wg.Wait()
	// End span so it gets reported via userdTraces
	span.End()
	// These go after the other traces so that we can capture traces from the gathering of traces itself
	err = c.userdTraces(ctx, tCh)
	if err != nil {
		// Can't imagine this makes a difference, since we've failed to collect it, but we may as well record it
		err = fmt.Errorf("failed to collect user daemon traces: %v\n", err)
		span.RecordError(err)
		dlog.Error(ctx, err)
	}

	close(tCh)
	err = <-errCh
	if err != nil {
		return err
	}
	return nil
}
