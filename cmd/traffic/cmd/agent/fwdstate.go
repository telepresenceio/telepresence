package agent

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type fwdState struct {
	*state
	intercept         agentconfig.InterceptTarget
	container         string
	forwarder         forwarder.Interceptor
	chosenInterceptId string
}

// NewInterceptState creates an InterceptState that performs intercepts by using an Interceptor which indiscriminately
// intercepts all traffic to the port that it forwards.
func (s *state) NewInterceptState(forwarder forwarder.Interceptor, intercept agentconfig.InterceptTarget, container string) InterceptState {
	return &fwdState{
		state:     s,
		intercept: intercept,
		container: container,
		forwarder: forwarder,
	}
}

func (fs *fwdState) Target() agentconfig.InterceptTarget {
	return fs.intercept
}

func (fs *fwdState) InterceptInfo(ctx context.Context, callerID, path string, containerPort uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	// The OSS agent is either intercepting or it isn't. There's no way to tell what it is that's being intercepted.
	fw := fs.forwarder
	if containerPort == 0 {
		return fw.InterceptInfo(), nil
	}
	port := fw.Target().Port()
	if containerPort == port {
		return fw.InterceptInfo(), nil
	}
	dlog.Debugf(ctx, "no match found for path %q, port %d, %s", path, containerPort, headers)
	return &restapi.InterceptInfo{Intercepted: false}, nil
}

type ProviderMux struct {
	AgentProvider   tunnel.ClientStreamProvider
	ManagerProvider tunnel.StreamProvider
}

func (pm *ProviderMux) ReportMetrics(ctx context.Context, metrics *manager.TunnelMetrics) {
	pm.AgentProvider.ReportMetrics(ctx, metrics)
}

func (pm *ProviderMux) CreateClientStream(ctx context.Context, tag tunnel.Tag, sessionID tunnel.SessionID, id tunnel.ConnID, roundTripLatency, dialTimeout time.Duration,
) (tunnel.Stream, error) {
	return pm.AgentProvider.CreateClientStream(ctx, tag, sessionID, id, roundTripLatency, dialTimeout)
}

func (fs *fwdState) HandlePort(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	dlog.Debugf(ctx, "fwdState.HandlePort called with %d intercepts", len(cepts))

	var active []*manager.InterceptInfo
	var waiting []*manager.InterceptInfo
	for _, is := range cepts {
		switch is.Disposition {
		case manager.InterceptDispositionType_ACTIVE:
			active = append(active, is)
		case manager.InterceptDispositionType_WAITING:
			waiting = append(waiting, is)
		}
	}

	var activeIntercept *manager.InterceptInfo
	if fs.chosenInterceptId != "" {
		for _, is := range active {
			if fs.chosenInterceptId == is.Id {
				if !is.Spec.Wiretap {
					activeIntercept = is
				}
				break
			}
		}
	}

	if activeIntercept == nil {
		fs.chosenInterceptId = ""

		// Attach to already ACTIVE intercept if there is one.
		for _, is := range active {
			if !is.Spec.Wiretap {
				fs.chosenInterceptId = is.Id
				activeIntercept = is
				break
			}
		}
	}

	fwd := fs.forwarder
	if fs.sessionInfo != nil {
		// Update forwarding.
		fwd.SetStreamProvider(fs)
	}
	fwd.SetIntercepting(ctx, activeIntercept)

	// Remove inactive wiretaps.
	for _, id := range fwd.WiretapIDs() {
		if !slices.ContainsFunc(active, func(ii *manager.InterceptInfo) bool { return ii.Id == id && ii.Spec.Wiretap }) {
			dlog.Debugf(ctx, "removing wiretap id %s", id)
			fwd.RemoveWiretap(id)
		}
	}

	// Add active wiretaps.
	for _, ii := range active {
		if ii.Spec.Wiretap {
			if !fwd.HasWiretap(ii.Id) {
				dlog.Debugf(ctx, "adding wiretap id %s to %s", ii.Id, iputil.JoinHostPort(ii.Spec.TargetHost, uint16(ii.Spec.TargetPort)))
				fwd.AddWiretap(ii)
			}
		}
	}

	// Review waiting intercepts
	reviews := make([]*manager.ReviewInterceptRequest, 0, len(waiting))
	for _, ii := range waiting {
		switch {
		case activeIntercept == nil || ii.Spec.Wiretap:
			// This intercept is ready to be active
			container := ii.Spec.ContainerName
			if container == "" {
				container = fs.container
			}
			cs := fs.containerStates[container]
			if cs == nil {
				reviews = append(reviews, &manager.ReviewInterceptRequest{
					Id:                ii.Id,
					Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
					Message:           fmt.Sprintf("No match for container %q", container),
					MechanismArgsDesc: "all TCP connections",
				})
				continue
			}
			if !ii.Spec.Wiretap {
				// We can only have one active intercept that isn't a wiretap
				activeIntercept = ii
			}
			reviews = append(reviews, &manager.ReviewInterceptRequest{
				Id:                ii.Id,
				Disposition:       manager.InterceptDispositionType_ACTIVE,
				PodIp:             fs.PodIP().String(),
				FtpPort:           int32(fs.FtpPort()),
				SftpPort:          int32(fs.SftpPort()),
				MountPoint:        cs.MountPoint(),
				Mounts:            cs.Mounts().ToRPC(),
				MechanismArgsDesc: "all TCP connections",
				Environment:       cs.Env(),
			})
		default:
			// We already have an intercept in play, so reject this one.
			chosenID := activeIntercept.Id
			dlog.Infof(ctx, "Setting intercept %q as AGENT_ERROR; as it conflicts with %q as the current chosen-to-be-ACTIVE intercept", ii.Id, chosenID)
			var msg string
			if activeIntercept.Disposition == manager.InterceptDispositionType_ACTIVE {
				msg = fmt.Sprintf("Conflicts with the currently-served intercept %q", chosenID)
			} else {
				msg = fmt.Sprintf("Conflicts with the currently-waiting-to-be-served intercept %q", chosenID)
			}
			reviews = append(reviews, &manager.ReviewInterceptRequest{
				Id:                ii.Id,
				Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
				Message:           msg,
				MechanismArgsDesc: "all TCP connections",
			})
		}
	}
	return reviews
}
