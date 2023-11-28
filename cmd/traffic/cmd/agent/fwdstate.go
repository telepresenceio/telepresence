package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type fwdState struct {
	*simpleState
	intercept  InterceptTarget
	forwarder  forwarder.Interceptor
	mountPoint string
	env        map[string]string
}

// NewInterceptState creates a InterceptState that performs intercepts by using an Interceptor which indiscriminately
// intercepts all traffic to the port that it forwards.
func (s *simpleState) NewInterceptState(forwarder forwarder.Interceptor, intercept InterceptTarget, mountPoint string, env map[string]string) InterceptState {
	return &fwdState{
		simpleState: s,
		mountPoint:  mountPoint,
		intercept:   intercept,
		forwarder:   forwarder,
		env:         env,
	}
}

func (fs *fwdState) Target() InterceptTarget {
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

type ProviderMux struct {
	AgentProvider   tunnel.ClientStreamProvider
	ManagerProvider tunnel.StreamProvider
}

func (pm *ProviderMux) ReportMetrics(ctx context.Context, metrics *manager.TunnelMetrics) {
	pm.AgentProvider.ReportMetrics(ctx, metrics)
}

func (pm *ProviderMux) CreateClientStream(ctx context.Context, sessionID string, id tunnel.ConnID, roundTripLatency, dialTimeout time.Duration) (tunnel.Stream, error) {
	s, err := pm.AgentProvider.CreateClientStream(ctx, sessionID, id, roundTripLatency, dialTimeout)
	if err == nil && s == nil {
		s, err = pm.ManagerProvider.CreateClientStream(ctx, sessionID, id, roundTripLatency, dialTimeout)
	}
	return s, err
}

func (fs *fwdState) HandleIntercepts(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	var myChoice, activeIntercept *manager.InterceptInfo

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

	if fs.sessionInfo != nil {
		// Update forwarding.
		fs.forwarder.SetStreamProvider(
			&ProviderMux{
				AgentProvider:   fs,
				ManagerProvider: &tunnel.TrafficManagerStreamProvider{Manager: fs.ManagerClient(), AgentSessionID: fs.sessionInfo.SessionId},
			})
	}
	fs.forwarder.SetIntercepting(activeIntercept)

	// Review waiting intercepts
	reviews := make([]*manager.ReviewInterceptRequest, 0, len(cepts))
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
					FtpPort:           int32(fs.FtpPort()),
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
					FtpPort:           int32(fs.FtpPort()),
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
