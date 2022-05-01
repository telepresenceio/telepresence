package agent

import (
	"context"
	"fmt"
	"net/http"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type fwdState struct {
	*simpleState
	intercept  *agentconfig.Intercept
	forwarder  *forwarder.Forwarder
	mountPoint string
	env        map[string]string
}

// NewInterceptState creates a InterceptState that performs intercepts by using a forwarder.Forwarder. A forwarder will indiscriminately
// intercept all traffic to the port that it forwards.
func NewInterceptState(s State, forwarder *forwarder.Forwarder, intercept *agentconfig.Intercept, mountPoint string, env map[string]string) InterceptState {
	return &fwdState{
		simpleState: s.(*simpleState),
		mountPoint:  mountPoint,
		intercept:   intercept,
		forwarder:   forwarder,
		env:         env,
	}
}

func (fs *fwdState) InterceptConfig() *agentconfig.Intercept {
	return fs.intercept
}

func (fs *fwdState) InterceptInfo(ctx context.Context, callerID, path string, containerPort uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	// The OSS agent is either intercepting or it isn't. There's no way to tell what it is that's being intercepted.
	fw := fs.forwarder
	if containerPort == 0 {
		return fw.InterceptInfo(), nil
	}
	_, port := fw.Target()
	if containerPort == port {
		return fw.InterceptInfo(), nil
	}
	portInfo := ""
	if containerPort != 0 {
		portInfo = fmt.Sprintf(", port %d", containerPort)
	}
	dlog.Debugf(ctx, "no match found for path %q%s, %s", path, portInfo, headers)
	return &restapi.InterceptInfo{Intercepted: false}, nil
}

func (fs *fwdState) HandleIntercepts(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	var myChoice, activeIntercept *manager.InterceptInfo

	dlog.Debug(ctx, "HandleIntercepts called")

	// Find the chosen intercept if it still exists
	if fs.chosenIntercept != nil {
		for _, cept := range cepts {
			if cept == fs.chosenIntercept {
				myChoice = cept
				break
			}
		}

		if myChoice != nil && myChoice.Disposition == manager.InterceptDispositionType_ACTIVE {
			// The chosen intercept still exists and is active
			activeIntercept = myChoice
		}
	} else {
		// Attach to already ACTIVE intercept if there is one.
		for _, cept := range cepts {
			if cept.Disposition == manager.InterceptDispositionType_ACTIVE {
				myChoice = cept
				fs.chosenIntercept = cept
				activeIntercept = cept
				break
			}
		}
	}

	// Update forwarding.
	fs.forwarder.SetManager(fs.SessionInfo(), fs.ManagerClient(), fs.ManagerVersion())
	fs.forwarder.SetIntercepting(activeIntercept)

	// Review waiting intercepts
	reviews := []*manager.ReviewInterceptRequest{}
	for _, cept := range cepts {
		if cept.Disposition == manager.InterceptDispositionType_WAITING {
			// This intercept is ready to be active
			switch {
			case cept == myChoice:
				// We've already chosen this one, but it's not active yet in this
				// snapshot. Let's go ahead and tell the manager to mark it ACTIVE.
				dlog.Infof(ctx, "Setting intercept %q as ACTIVE (again?)", cept.Id)
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:                cept.Id,
					Disposition:       manager.InterceptDispositionType_ACTIVE,
					PodIp:             fs.PodIP(),
					SftpPort:          int32(fs.SftpPort()),
					MountPoint:        fs.mountPoint,
					MechanismArgsDesc: "all TCP connections",
					Environment:       fs.env,
				})
			case fs.chosenIntercept == nil:
				// We don't have an intercept in play, so choose this one. All
				// agents will get intercepts in the same order every time, so
				// this will yield a consistent result. Note that the intercept
				// will not become active at this time. That will happen later,
				// once the manager assigns a port.
				dlog.Infof(ctx, "Setting intercept %q as ACTIVE", cept.Id)
				fs.chosenIntercept = cept
				myChoice = cept
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:                cept.Id,
					Disposition:       manager.InterceptDispositionType_ACTIVE,
					PodIp:             fs.PodIP(),
					SftpPort:          int32(fs.SftpPort()),
					MountPoint:        fs.mountPoint,
					MechanismArgsDesc: "all TCP connections",
					Environment:       fs.env,
				})
			default:
				// We already have an intercept in play, so reject this one.
				chosenID := fs.chosenIntercept.Id
				dlog.Infof(ctx, "Setting intercept %q as AGENT_ERROR; as it conflicts with %q as the current chosen-to-be-ACTIVE intercept", cept.Id, chosenID)
				var msg string
				if fs.chosenIntercept.Disposition == manager.InterceptDispositionType_ACTIVE {
					msg = fmt.Sprintf("Conflicts with the currently-served intercept %q", chosenID)
				} else {
					msg = fmt.Sprintf("Conflicts with the currently-waiting-to-be-served intercept %q", chosenID)
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
