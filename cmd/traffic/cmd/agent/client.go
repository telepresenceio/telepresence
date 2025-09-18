package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
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

const watchRetryInterval = 2 * time.Second

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
			dlog.Errorf(ctx, "depart session: %+v", err)
		}
	}()

	wg := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		SoftShutdownTimeout: time.Second * 10,
		HardShutdownTimeout: time.Second * 10,
	})

	wg.Go("logLevelWatch", func(ctx context.Context) error {
		return logLevelWatchLoop(ctx, manager)
	})
	snapshots := make(chan *rpc.InterceptInfoSnapshot)
	wg.Go("interceptWatch", func(ctx context.Context) error {
		return interceptWatchLoop(ctx, manager, session, info, snapshots)
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

func logLevelWatchLoop(ctx context.Context, manager rpc.ManagerClient) error {
	timedLevel := log.NewTimedLevel(log.DlogLevelNames[dlog.MaxLogLevel(ctx)], log.SetLevel)
	return watcher.WatchWithRetry(ctx, "WatchLogLevel", watchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[rpc.LogLevelRequest], error) {
			return manager.WatchLogLevel(ctx, &empty.Empty{})
		},
		func(ll *rpc.LogLevelRequest) error {
			duration := time.Duration(0)
			if ll.Duration != nil {
				duration = ll.Duration.AsDuration()
			}
			timedLevel.Set(ctx, ll.LogLevel, duration)
			return nil
		},
		nil,
	)
}

func interceptWatchLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, info *rpc.AgentInfo, snapshots chan<- *rpc.InterceptInfoSnapshot) error {
	// Call WatchIntercepts and publish the snapshots on the channel
	return watcher.WatchWithRetry(ctx, "WatchIntercepts", watchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[rpc.InterceptInfoSnapshot], error) {
			return manager.WatchIntercepts(ctx, session)
		},
		func(snapshot *rpc.InterceptInfoSnapshot) error {
			snapshots <- snapshot
			return nil
		},
		nil,
	)
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
			dlog.Warnf(ctx, "remain: %v", err)
		}
	}
}

func handleInterceptLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, snapshots <-chan *rpc.InterceptInfoSnapshot, state State) error {
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
