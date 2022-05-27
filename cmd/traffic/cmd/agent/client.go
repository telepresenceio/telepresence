package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/blang/semver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func GetAmbassadorCloudConnectionInfo(ctx context.Context, address string) (*rpc.AmbassadorCloudConnection, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return &rpc.AmbassadorCloudConnection{}, err
	}
	defer conn.Close()

	manager := rpc.NewManagerClient(conn)
	cloudConnectInfo, err := manager.CanConnectAmbassadorCloud(ctx, &empty.Empty{})
	if err != nil {
		return &rpc.AmbassadorCloudConnection{}, err
	}
	return cloudConnectInfo, nil
}

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

func TalkToManager(ctx context.Context, address string, info *rpc.AgentInfo, state State) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return err
	}
	defer conn.Close()

	manager := rpc.NewManagerClient(conn)

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

	state.SetManager(session, manager, mgrVer)

	// Create the /tmp/agent directory if it doesn't exist
	// We use this to place a file which conveys 'readiness'
	// The presence of this file is used in the readiness check.
	dir := "/tmp/agent"
	if _, err := dos.Stat(ctx, dir); os.IsNotExist(err) {
		if err := dos.Mkdir(ctx, "/tmp/agent", 0777); err != nil {
			return err
		}
	}
	defer func() {
		// The ctx might well be cancelled at this point but is used as parent during
		// the timed clean-up to keep logging intact.
		ctx, cancel := context.WithTimeout(dcontext.WithoutCancel(ctx), time.Second)
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

	// Deal with host lookups dispatched to this agent during intercepts
	lrStream, err := manager.WatchLookupHost(ctx, session)
	if err != nil {
		return err
	}

	wg := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	wg.Go("lookupHostWait", func(ctx context.Context) error {
		return lookupHostWaitLoop(ctx, manager, session, lrStream)
	})

	// Deal with dial requests from the manager
	dialerStream, err := manager.WatchDial(ctx, session)
	if err != nil {
		return err
	}
	wg.Go("dialWait", func(ctx context.Context) error {
		return tunnel.DialWaitLoop(ctx, manager, dialerStream, session.SessionId)
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

	file, err := dos.OpenFile(ctx, "/tmp/agent/ready", os.O_CREATE|os.O_WRONLY, 0666)
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

func lookupHostWaitLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, lookupHostStream rpc.Manager_WatchLookupHostClient) error {
	for ctx.Err() == nil {
		lr, err := lookupHostStream.Recv()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("lookup request stream recv: %w", err)
			}
			return nil
		}
		go lookupAndRespond(ctx, manager, session, lr)
	}
	return nil
}

func lookupAndRespond(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, lr *rpc.LookupHostRequest) {
	dlog.Debugf(ctx, "LookupRequest for %s", lr.Host)
	response := rpc.LookupHostAgentResponse{
		Session:  session,
		Request:  lr,
		Response: &rpc.LookupHostResponse{},
	}

	addrs, err := net.DefaultResolver.LookupHost(ctx, lr.Host)
	switch {
	case err != nil:
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			dlog.Debugf(ctx, "Lookup response for %s -> NOT FOUND", lr.Host)
		} else {
			dlog.Errorf(ctx, "LookupHost: %v", err)
		}
	case len(addrs) > 0:
		ips := make(iputil.IPs, len(addrs))
		for i, addr := range addrs {
			ips[i] = iputil.Parse(addr)
		}
		dlog.Debugf(ctx, "Lookup response for %s -> %s", lr.Host, ips)
		response.Response.Ips = ips.BytesSlice()
	default:
		dlog.Debugf(ctx, "Lookup response for %s -> EMPTY", lr.Host)
	}
	if _, err = manager.AgentLookupHostResponse(ctx, &response); err != nil {
		if ctx.Err() == nil {
			dlog.Debugf(ctx, "lookup response: %+v %v", err, &response)
		}
	}
}

// GetLogLevel will return the log level that this agent should use
func GetLogLevel(ctx context.Context) string {
	level, ok := dos.LookupEnv(ctx, install.EnvPrefix+"LOG_LEVEL")
	if !ok {
		level = dos.Getenv(ctx, "LOG_LEVEL")
	}
	return level
}

func logLevelWaitLoop(ctx context.Context, logLevelStream rpc.Manager_WatchLogLevelClient) error {
	level := GetLogLevel(ctx)
	timedLevel := log.NewTimedLevel(level, log.SetLevel)
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
