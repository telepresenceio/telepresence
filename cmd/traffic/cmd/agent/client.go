package agent

import (
	"context"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func GetAmbassadorCloudConnectionInfo(ctx context.Context, address string) (*rpc.AmbassadorCloudConnection, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address, grpc.WithInsecure(), grpc.WithBlock())
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

func TalkToManager(ctx context.Context, address string, info *rpc.AgentInfo, state State) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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

	state.SetManager(session, manager)

	// Create the /tmp/agent directory if it doesn't exist
	// We use this to place a file which conveys 'readiness'
	// The presence of this file is used in the readiness check.
	dir := "/tmp/agent"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.Mkdir("/tmp/agent", 0777); err != nil {
			return err
		}
	}
	file, err := os.OpenFile("/tmp/agent/ready", os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer file.Close()

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
	go lookupHostWaitLoop(ctx, manager, session, lrStream)

	// Loop calling Remain
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	snapshots := make(chan *rpc.InterceptInfoSnapshot)

	// Call WatchIntercepts
	stream, err := manager.WatchIntercepts(ctx, session)
	if err != nil {
		return err
	}
	go interceptWaitLoop(ctx, cancel, snapshots, stream)

	for {
		select {
		case <-ctx.Done():
			return nil
		case snapshot := <-snapshots:
			reviews := state.HandleIntercepts(ctx, snapshot.Intercepts)
			for _, review := range reviews {
				review.Session = session
				if _, err := manager.ReviewIntercept(ctx, review); err != nil {
					return err
				}
			}
		case <-ticker.C:
		}

		if _, err := manager.Remain(ctx, &rpc.RemainRequest{Session: session}); err != nil {
			return err
		}
	}
}

func interceptWaitLoop(ctx context.Context, cancel context.CancelFunc, snapshots chan<- *rpc.InterceptInfoSnapshot, stream rpc.Manager_WatchInterceptsClient) {
	defer cancel() // Drop the gRPC connection if we leave this function
	for {
		snapshot, err := stream.Recv()
		if err != nil {
			dlog.Errorf(ctx, "stream Recv: %+v", err) // May be io.EOF
			return
		}
		snapshots <- snapshot
	}
}

func lookupHostWaitLoop(ctx context.Context, manager rpc.ManagerClient, session *rpc.SessionInfo, lookupHostStream rpc.Manager_WatchLookupHostClient) {
	for ctx.Err() == nil {
		lr, err := lookupHostStream.Recv()
		if err != nil {
			if ctx.Err() == nil {
				dlog.Debugf(ctx, "lookup request stream recv: %+v", err) // May be io.EOF
			}
			return
		}
		dlog.Debugf(ctx, "LookupRequest for %s", lr.Host)
		addrs, err := net.LookupHost(lr.Host)
		r := rpc.LookupHostResponse{}
		if err == nil {
			ips := make(iputil.IPs, len(addrs))
			for i, addr := range addrs {
				ips[i] = iputil.Parse(addr)
			}
			dlog.Debugf(ctx, "Lookup response for %s -> %s", lr.Host, ips)
			r.Ips = ips.BytesSlice()
		}
		response := rpc.LookupHostAgentResponse{
			Session:  session,
			Request:  lr,
			Response: &r,
		}
		if _, err = manager.AgentLookupHostResponse(ctx, &response); err != nil {
			if ctx.Err() == nil {
				dlog.Debugf(ctx, "lookup response: %+v %v", err, &response)
			}
			return
		}
	}
}
