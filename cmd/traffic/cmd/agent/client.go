package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
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

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
	clog.Infof(ctx, "Connected to Manager %s", verStr)
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
	if _, err := dos.Stat(ctx, dir); errors.Is(err, fs.ErrNotExist) {
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
			clog.Errorf(ctx, "depart session: %+v", err)
		}
	}()

	wg := log.NewGroup(ctx)

	retryInterval := state.AgentConfig().WatchRetryInterval
	wg.Go("logLevelWatch", func(ctx context.Context) error {
		return logLevelWatchLoop(ctx, state.AgentConfig().LogLevel, manager, retryInterval)
	})
	snapshots := make(chan []*rpc.InterceptInfo)
	wg.Go("interceptWatch", func(ctx context.Context) error {
		return interceptWatchLoop(ctx, manager, session, info, snapshots, retryInterval)
	})
	wg.Go("handleIntercept", func(ctx context.Context) error {
		return handleInterceptLoop(ctx, manager, session, snapshots, state)
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

func logLevelWatchLoop(ctx context.Context, level slog.Level, manager rpc.ManagerClient, retryInterval time.Duration) error {
	timedLevel := log.NewTimedLevel(level, clog.SetTreeLevel)
	return watcher.WatchWithRetry(ctx, "WatchLogLevel", retryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[rpc.LogLevelRequest], error) {
			return manager.WatchLogLevel(ctx, &empty.Empty{})
		},
		func(ll *rpc.LogLevelRequest) error {
			lvl, err := clog.ParseLevel(ll.LogLevel)
			if err != nil {
				return err
			}
			duration := time.Duration(0)
			if ll.Duration != nil {
				duration = ll.Duration.AsDuration()
			}
			timedLevel.Set(ctx, lvl, duration)
			return nil
		},
		nil,
	)
}

func interceptWatchLoop(
	ctx context.Context,
	manager rpc.ManagerClient,
	session *rpc.SessionInfo,
	info *rpc.AgentInfo,
	snapshots chan<- []*rpc.InterceptInfo,
	retryInterval time.Duration,
) error {
	reconnectAgent := func() error {
		_, err := manager.ReconnectAgent(ctx, &rpc.ReconnectAgentRequest{
			Session: session,
			Agent:   info,
		})
		return err
	}
	// Call WatchIntercepts and publish the snapshots on the channel
	snapMap := make(map[string]*rpc.InterceptInfo)
	err := watcher.WatchWithRetry(ctx, "WatchInterceptsDelta", retryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[rpc.InterceptInfoDelta], error) {
			return manager.WatchInterceptsDelta(ctx, session)
		},
		func(delta *rpc.InterceptInfoDelta) error {
			maps.DeltaUpdate(snapMap, delta.Upserts, delta.Removals)
			snapshots <- maps.Values(snapMap)
			return nil
		}, reconnectAgent)
	if err != nil && status.Code(err) == codes.Unimplemented {
		// Fall back to streaming all intercepts if the traffic manager doesn't support delta updates.'
		clog.Warnf(ctx, "WatchInterceptsDelta is not implemented by the traffic-manager, falling back to WatchIntercepts and full snapshots")
		err = watcher.WatchWithRetry(ctx, "WatchIntercepts", retryInterval,
			func(ctx context.Context) (grpc.ServerStreamingClient[rpc.InterceptInfoSnapshot], error) {
				return manager.WatchIntercepts(ctx, session)
			},
			func(snapshot *rpc.InterceptInfoSnapshot) error {
				snapshots <- snapshot.Intercepts
				return nil
			}, reconnectAgent)
	}
	return err
}

func remainLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo) error {
	// Loop calling Remain
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if _, err := manager.Remain(ctx, &rpc.RemainRequest{Session: session}); err != nil {
			clog.Warnf(ctx, "remain: %v", err)
		}
	}
}

func handleInterceptLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, snapshots <-chan []*rpc.InterceptInfo, state State) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case snapshot := <-snapshots:
			clog.Debugf(ctx, "HandleIntercepts %s", interceptsStringer(snapshot))
			reviews := state.HandleIntercepts(ctx, snapshot)
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
