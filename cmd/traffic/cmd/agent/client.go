package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/blang/semver"
	dns2 "github.com/miekg/dns"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type interceptsStringer []*rpc.InterceptInfo

func (is interceptsStringer) String() string {
	sb := strings.Builder{}
	sb.WriteByte('[')
	for i, ii := range is {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(ii.Id)
		sb.WriteByte(' ')
		sb.WriteString(ii.Disposition.String())
	}
	sb.WriteByte(']')
	return sb.String()
}

var NewExtendedManagerClient func(conn *grpc.ClientConn, ossManager rpc.ManagerClient) rpc.ManagerClient //nolint:gochecknoglobals // extension point

func TalkToManager(ctx context.Context, address string, info *rpc.AgentInfo, state State) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	manager := rpc.NewManagerClient(conn)
	if NewExtendedManagerClient != nil {
		manager = NewExtendedManagerClient(conn, manager)
	}

	ver, err := manager.Version(ctx, &empty.Empty{})
	if err != nil {
		return err
	}

	verStr := strings.TrimPrefix(ver.Version, "v")
	dlog.Infof(ctx, "Connected to Manager %s", verStr)
	mgrVer, err := semver.Parse(verStr)
	if err != nil {
		return fmt.Errorf("failed to parse manager version %q: %s", verStr, err)
	}

	session, err := manager.ArriveAsAgent(ctx, info)
	if err != nil {
		return err
	}

	state.SetManager(ctx, session, manager, mgrVer)

	// Create the /tmp/agent directory if it doesn't exist
	// We use this to place a file which conveys 'readiness'
	// The presence of this file is used in the readiness check.
	dir := "/tmp/agent"
	if _, err := dos.Stat(ctx, dir); os.IsNotExist(err) {
		if err := dos.Mkdir(ctx, "/tmp/agent", 0o777); err != nil {
			return err
		}
	}
	defer func() {
		// The ctx might well be cancelled at this point but is used as parent during
		// the timed clean-up to keep logging intact.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()

		// Reset state by processing an empty snapshot
		// - clear out any intercepts
		// - set forwarding to the app
		state.HandleIntercepts(ctx, nil)

		// Depart session
		if _, err := manager.Depart(ctx, session); err != nil {
			dlog.Errorf(ctx, "depart session: %+v", err)
		}
	}()

	wg := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		SoftShutdownTimeout: time.Second * 10,
		HardShutdownTimeout: time.Second * 10,
	})

	// Deal with DNS lookups dispatched to this agent during intercepts
	dnsStream, err := manager.WatchLookupDNS(ctx, session)
	if err != nil {
		return err
	}
	wg.Go("lookupDNSWait", func(ctx context.Context) error {
		return lookupDNSWaitLoop(ctx, manager, session, dnsStream)
	})

	// Deal with dial requests from the manager
	dialerStream, err := manager.WatchDial(ctx, session)
	if err != nil {
		return err
	}
	wg.Go("dialWait", func(ctx context.Context) error {
		return tunnel.DialWaitLoop(ctx, tunnel.ManagerProvider(manager), dialerStream, session.SessionId)
	})

	// Deal with log-level changes
	logLevelStream, err := manager.WatchLogLevel(ctx, &empty.Empty{})
	if err != nil {
		return err
	}
	wg.Go("logLevelWait", func(ctx context.Context) error {
		return logLevelWaitLoop(ctx, logLevelStream)
	})

	snapshots := make(chan *rpc.InterceptInfoSnapshot)

	// Call WatchIntercepts
	stream, err := manager.WatchIntercepts(ctx, session)
	if err != nil {
		return err
	}
	wg.Go("interceptWait", func(ctx context.Context) error {
		return interceptWaitLoop(ctx, cancel, snapshots, stream)
	})
	wg.Go("handleIntercept", func(ctx context.Context) error {
		return handleInterceptLoop(ctx, snapshots, state, manager, session)
	})
	wg.Go("remain", func(ctx context.Context) error {
		return remainLoop(ctx, manager, session)
	})

	file, err := dos.OpenFile(ctx, "/tmp/agent/ready", os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	_ = file.Close()
	return wg.Wait()
}

func remainLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo) error {
	// Loop calling Remain
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if _, err := manager.Remain(ctx, &rpc.RemainRequest{Session: session}); err != nil {
			return err
		}
	}
}

func handleInterceptLoop(ctx context.Context, snapshots <-chan *rpc.InterceptInfoSnapshot, state State, manager rpc.ManagerClient, session *rpc.SessionInfo) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case snapshot := <-snapshots:
			dlog.Debugf(ctx, "HandleIntercepts %s", interceptsStringer(snapshot.Intercepts))
			reviews := state.HandleIntercepts(ctx, snapshot.Intercepts)
			for _, review := range reviews {
				review.Session = session
				if _, err := manager.ReviewIntercept(ctx, review); err != nil {
					if status.Code(err) == codes.NotFound {
						// An intercept may be removed after a snapshot has arrived and before
						// the next snapshot. This is not an error. We can safely assume that
						// a new snapshot will arrive.
						continue
					}
					return err
				}
			}
		}
	}
}

func interceptWaitLoop(ctx context.Context, cancel context.CancelFunc, snapshots chan<- *rpc.InterceptInfoSnapshot, stream rpc.Manager_WatchInterceptsClient) error {
	defer cancel() // Drop the gRPC connection if we leave this function
	for {
		snapshot, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("stream Recv: %w", err)
			}
			return nil
		}
		snapshots <- snapshot
	}
}

func lookupDNSWaitLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, lookupDNSStream rpc.Manager_WatchLookupDNSClient) error {
	for ctx.Err() == nil {
		lr, err := lookupDNSStream.Recv()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("lookup request stream recv: %w", err)
			}
			return nil
		}
		go lookupDNSAndRespond(ctx, manager, session, lr)
	}
	return nil
}

func lookupDNSAndRespond(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, lr *rpc.DNSRequest) {
	qType := uint16(lr.Type)
	tqn := dns2.TypeToString[qType]
	rrs, rCode, err := dnsproxy.Lookup(ctx, qType, lr.Name)
	if err != nil {
		dlog.Errorf(ctx, "LookupDNS %s %s: %v", lr.Name, tqn, err)
		return
	}
	res, err := dnsproxy.ToRPC(rrs, rCode)
	if err != nil {
		dlog.Errorf(ctx, "ToRPC %s %s: %v", lr.Name, tqn, err)
		return
	}
	if len(rrs) > 0 {
		dlog.Debugf(ctx, "LookupDNS %s %s -> %v", lr.Name, tqn, rrs)
	} else {
		dlog.Debugf(ctx, "LookupDNS %s %s -> EMPTY", lr.Name, tqn)
	}
	if _, err := manager.AgentLookupDNSResponse(ctx, &rpc.DNSAgentResponse{Session: session, Request: lr, Response: res}); err != nil {
		if ctx.Err() == nil {
			dlog.Errorf(ctx, "AgentLookupDNSResponse: %v", err)
		}
	}
}

func logLevelWaitLoop(ctx context.Context, logLevelStream rpc.Manager_WatchLogLevelClient) error {
	timedLevel := log.NewTimedLevel(log.DlogLevelNames[dlog.MaxLogLevel(ctx)], log.SetLevel)
	for ctx.Err() == nil {
		ll, err := logLevelStream.Recv()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("log-level stream recv: %w", err)
			}
			return nil
		}
		duration := time.Duration(0)
		if ll.Duration != nil {
			duration = ll.Duration.AsDuration()
		}
		timedLevel.Set(ctx, ll.LogLevel, duration)
	}
	return nil
}
