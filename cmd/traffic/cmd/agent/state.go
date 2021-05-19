package agent

import (
	"context"
	"fmt"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type State interface {
	HandleIntercepts(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest
}

// State of the Traffic Agent.
type state struct {
	forwarder   *Forwarder
	managerHost string
	appHost     string
	appPort     int32
	chosenID    string
	namespace   string
	podIP       string
	sftpPort    int32
}

func NewState(forwarder *Forwarder, managerHost, namespace, podIP string, sftpPort int32) State {
	host, port := forwarder.Target()
	return &state{
		forwarder:   forwarder,
		managerHost: managerHost,
		appHost:     host,
		appPort:     port,
		namespace:   namespace,
		podIP:       podIP,
		sftpPort:    sftpPort,
	}
}

func (s *state) HandleIntercepts(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	var chosenIntercept, activeIntercept *manager.InterceptInfo

	dlog.Debug(ctx, "HandleIntercepts called")

	// Find the chosen intercept if it still exists
	if s.chosenID != "" {
		for _, cept := range cepts {
			if cept.Id == s.chosenID {
				chosenIntercept = cept
				break
			}
		}
	}

	if chosenIntercept != nil {
		// The chosen intercept still exists
		if chosenIntercept.Disposition == manager.InterceptDispositionType_ACTIVE {
			// and is active
			activeIntercept = chosenIntercept
		}
	} else {
		// The chosen intercept was deleted by the user
		dlog.Info(ctx, "The previously-active intercept has been deleted")
		s.chosenID = ""
	}

	// Update forwarding
	if activeIntercept != nil {
		s.forwarder.SetTarget(s.managerHost, activeIntercept.ManagerPort)
	} else {
		s.forwarder.SetTarget(s.appHost, s.appPort)
	}

	// Review waiting intercepts
	reviews := []*manager.ReviewInterceptRequest{}
	for i, cept := range cepts {
		if cept.Disposition == manager.InterceptDispositionType_WAITING {
			// This intercept is ready to be active
			switch {
			case chosenIntercept != nil && cept.Id == chosenIntercept.Id:
				// We've already chosen this one and marked it active, but it's not
				// active yet in this snapshot.  We could probably just do nothing
				// and it would probably change to ACTIVE in the very next snapshot
				// because we already marked it active from a previous snapshot and
				// that just hasn't propagated yet.  But let's go ahead and tell the
				// manager to mark it ACTIVE again anyway, just to be safe.
				dlog.Infof(ctx, "Setting intercept %q as ACTIVE (again?)", cept.Id)
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:                cept.Id,
					Disposition:       manager.InterceptDispositionType_ACTIVE,
					PodIp:             s.podIP,
					SftpPort:          s.sftpPort,
					MechanismArgsDesc: "all TCP connections",
				})
			case chosenIntercept == nil:
				// We don't have an intercept in play, so choose this one. All
				// agents will get intercepts in the same order every time, so
				// this will yield a consistent result. Note that the intercept
				// will not become active at this time. That will happen later,
				// once the manager assigns a port.
				dlog.Infof(ctx, "Setting intercept %q as ACTIVE", cept.Id)
				s.chosenID = cept.Id
				chosenIntercept = cepts[i]
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:                cept.Id,
					Disposition:       manager.InterceptDispositionType_ACTIVE,
					PodIp:             s.podIP,
					SftpPort:          s.sftpPort,
					MechanismArgsDesc: "all TCP connections",
				})
			default:
				// We already have an intercept in play, so reject this one.
				dlog.Infof(ctx, "Setting intercept %q as AGENT_ERROR; as it conflicts with %q as the current chosen-to-be-ACTIVE intercept", cept.Id, s.chosenID)
				var msg string
				if chosenIntercept.Disposition == manager.InterceptDispositionType_ACTIVE {
					msg = fmt.Sprintf("Conflicts with the currently-served intercept %q", s.chosenID)
				} else {
					msg = fmt.Sprintf("Conflicts with the currently-waiting-to-be-served intercept %q", s.chosenID)
				}
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:                cept.Id,
					Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
					Message:           msg,
					MechanismArgsDesc: "all TCP connections",
				})
			}
		}
	}

	return reviews
}
