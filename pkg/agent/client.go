package agent

import (
	"context"
	"time"

	"github.com/datawire/ambassador/pkg/dlog"
	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/rpc"
)

func TalkToManager(ctx context.Context, address string, info *rpc.AgentInfo) error {
	conn, err := grpc.DialContext(ctx, address, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		return err
	}
	defer conn.Close()

	manager := rpc.NewManagerClient(conn)

	ver, err := manager.Version(ctx, &empty.Empty{})
	if err != nil {
		return err
	}

	dlog.Infof(ctx, "Connected to Manager %s", ver.Version)

	session, err := manager.ArriveAsAgent(ctx, info)
	if err != nil {
		return err
	}

	defer func() {
		if _, err := manager.Depart(ctx, session); err != nil {
			dlog.Debugf(ctx, "depart session: %+v", err)
		}
	}()

	// Call WatchIntercepts
	stream, err := manager.WatchIntercepts(ctx, session)
	if err != nil {
		return err
	}

	snapshots := make(chan *rpc.InterceptInfoSnapshot)
	go func() {
		for {
			snapshot, err := stream.Recv()
			if err != nil {
				dlog.Debugf(ctx, "stream recv: %+v", err)
				// Assume we'll error out elsewhere too
				return
			}

			select {
			case <-stream.Context().Done():
				return
			case snapshots <- snapshot:
			}
		}
	}()

	// Loop calling Remain
	for {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		select {
		case <-ctx.Done():
			return nil
		case snapshot := <-snapshots:
			newPort := HandleIntercepts(snapshot.Intercepts)
			// FIXME actually do something...
			dlog.Infof(ctx, "Target port is now %d", newPort)
		case <-ticker.C:
		}

		if _, err := manager.Remain(ctx, session); err != nil {
			return err
		}
	}
}

// HandleIntercepts returns the Manager port to which to forward connections, or
// zero if no intercepts are in play.
func HandleIntercepts(cepts []*rpc.InterceptInfo) int {
	for _, cept := range cepts {
		if cept.Disposition == rpc.InterceptInfo_ACTIVE {
			return int(cept.ManagerPort)
		}
	}

	return 0
}
