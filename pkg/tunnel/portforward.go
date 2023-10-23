package tunnel

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

func TryPortForward(c context.Context, id ConnID, pf dnet.PortForwardDialer, mc manager.ManagerClient, sessionID string) Stream {
	pfr, err := mc.GetPortForwardPod(c, &manager.PortForwardPodRequest{ConnId: []byte(id)})
	switch status.Code(err) {
	case codes.OK:
		conn, err := pf.DialPod(c, pfr.Name, pfr.Namespace, uint16(pfr.Port))
		if err == nil {
			dlog.Debugf(c, "Using port-forward to %s.%s:%d for %s", pfr.Name, pfr.Namespace, pfr.Port, id.DestinationAddr())
			tc := client.GetConfig(c).Timeouts()
			return NewConnStream(conn, id, sessionID, tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
		}
		dlog.Debugf(c, "Unable to use direct-port forward to %s: DialPod returned %v", id.DestinationAddr(), err)
	case codes.Unimplemented:
		dlog.Debug(c, "Direct port forward is not implemented by the traffic-manager")
	case codes.Unavailable:
		dlog.Debug(c, "Direct port forward is disabled by the traffic-manager")
	default:
		dlog.Errorf(c, "Direct port forward: GetPortForwardPod errors with %v", err)
	}
	return nil
}
