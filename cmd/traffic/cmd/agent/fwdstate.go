package agent

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/fwd"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/icept"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type fwdState struct {
	*state
	intercept agentconfig.InterceptTarget
	container string
	forwarder fwd.Interceptor
}

// generateMechanismDescription creates a human-readable description for the intercept mechanism.
func generateMechanismDescription(spec *manager.InterceptSpec) string {
	return matcher.NewRequest(spec.PathFilters, spec.HeaderFilters).String()
}

// NewInterceptState creates an InterceptState that performs intercepts by using an Interceptor which indiscriminately
// intercepts all traffic to the port that it forwards.
func (s *state) NewInterceptState(forwarder fwd.Interceptor, intercept agentconfig.InterceptTarget, container string) InterceptState {
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
	fw := fs.forwarder
	r := &restapi.InterceptInfo{}
	if containerPort != 0 && containerPort != fs.intercept.ContainerPort() {
		clog.Debugf(ctx, "no match found for path %q, port %d, %s", path, containerPort, headers)
		return r, nil
	}
	for _, ii := range fw.InterceptInfos() {
		if callerID != "" && callerID != ii.Id {
			continue
		}
		if ii.Disposition == manager.InterceptDispositionType_ACTIVE {
			m := matcher.NewRequest(ii.Spec.PathFilters, ii.Spec.HeaderFilters)
			if m.MatchesPathAndHeader(path, headers) {
				r.Intercepted = true
				r.Metadata = ii.Spec.Metadata
				break
			}
		}
	}
	return r, nil
}

type ProviderMux struct {
	AgentProvider   tunnel.ClientStreamProvider
	ManagerProvider tunnel.StreamProvider
}

func (pm *ProviderMux) MetricsEnabled() bool {
	return pm.AgentProvider.MetricsEnabled()
}

func (pm *ProviderMux) ReportMetrics(ctx context.Context, metrics *manager.TunnelMetrics) {
	pm.AgentProvider.ReportMetrics(ctx, metrics)
}

func (pm *ProviderMux) CreateClientStream(ctx context.Context, tag tunnel.Tag, sessionID tunnel.SessionID, id tunnel.ConnID, roundTripLatency, dialTimeout time.Duration,
) (tunnel.Stream, error) {
	return pm.AgentProvider.CreateClientStream(ctx, tag, sessionID, id, roundTripLatency, dialTimeout)
}

// containerForIntercept returns the local container that should review and
// serve an intercept. A service-scoped intercept can be shared by workloads
// whose matching containers have different names, so the matched forwarder
// target wins over the primary workload's container name in that case.
func (fs *fwdState) containerForIntercept(spec *manager.InterceptSpec) string {
	if spec.ServiceUid != "" && fs.intercept.MatchForSpec(spec) {
		return fs.container
	}
	if spec.ContainerName != "" {
		return spec.ContainerName
	}
	return fs.container
}

// processWiretapIntercept handles wiretap intercepts which can always be active alongside others.
func (fs *fwdState) processWiretapIntercept(ii *manager.InterceptInfo) *manager.ReviewInterceptRequest {
	container := fs.containerForIntercept(ii.Spec)
	cs := fs.containerStates[container]
	if cs == nil {
		return &manager.ReviewInterceptRequest{
			Id:                ii.Id,
			Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
			Message:           fmt.Sprintf("No match for container %q", container),
			MechanismArgsDesc: generateMechanismDescription(ii.Spec),
		}
	}
	return &manager.ReviewInterceptRequest{
		Id:                ii.Id,
		Disposition:       manager.InterceptDispositionType_ACTIVE,
		PodIp:             fs.PodIP().String(),
		FtpPort:           int32(fs.FtpPort()),
		SftpPort:          int32(fs.SftpPort()),
		MountPoint:        cs.MountPoint(),
		Mounts:            cs.Mounts().ToRPC(),
		MechanismArgsDesc: generateMechanismDescription(ii.Spec),
		Environment:       cs.Env(),
	}
}

// findConflictingIntercept checks if an intercept conflicts with any active or waiting intercepts.
func (fs *fwdState) findConflictingIntercept(ii *manager.InterceptInfo, index int, active []*manager.InterceptInfo, candidates []*manager.InterceptInfo) *manager.InterceptInfo {
	// Check for conflicts with active intercepts
	for _, activeII := range active {
		if !activeII.Spec.Wiretap && icept.IsInConflict(ii.Spec, activeII.Spec) {
			return activeII
		}
	}

	// Check for conflicts with other waiting intercepts that would become active
	// Only check intercepts that come before this one (first wins policy)
	for j, otherII := range candidates {
		if j < index && !otherII.Spec.Wiretap && icept.IsInConflict(ii.Spec, otherII.Spec) {
			return otherII
		}
	}
	return nil
}

// processRegularIntercept handles non-wiretap intercepts with conflict detection.
func (fs *fwdState) processRegularIntercept(
	ctx context.Context,
	ii *manager.InterceptInfo,
	index int,
	active []*manager.InterceptInfo,
	candidates []*manager.InterceptInfo,
) *manager.ReviewInterceptRequest {
	conflictingIntercept := fs.findConflictingIntercept(ii, index, active, candidates)

	if conflictingIntercept != nil {
		// Reject due to actual conflict
		chosenID := conflictingIntercept.Id
		reason := icept.ExplainConflict(ii.Spec, conflictingIntercept.Spec)
		clog.Infof(ctx, "Setting intercept %q as AGENT_ERROR; as it conflicts with %q: %s", ii.Id, chosenID, reason)
		state := fmt.Sprintf("currently %s", strings.ToLower(conflictingIntercept.Disposition.String()))
		return &manager.ReviewInterceptRequest{
			Id:                ii.Id,
			Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
			Message:           fmt.Sprintf("conflicts with the %s intercept %q (%q): %s", state, chosenID, conflictingIntercept.Spec.Client, reason),
			MechanismArgsDesc: generateMechanismDescription(ii.Spec),
		}
	}

	// No conflict detected, allow this intercept to become active
	container := fs.containerForIntercept(ii.Spec)
	cs := fs.containerStates[container]
	if cs == nil {
		return &manager.ReviewInterceptRequest{
			Id:                ii.Id,
			Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
			Message:           fmt.Sprintf("No match for container %q", container),
			MechanismArgsDesc: generateMechanismDescription(ii.Spec),
		}
	}
	clog.Infof(ctx, "Allowing non-conflicting intercept %q to become active", ii.Id)
	return &manager.ReviewInterceptRequest{
		Id:                ii.Id,
		Disposition:       manager.InterceptDispositionType_ACTIVE,
		PodIp:             fs.PodIP().String(),
		FtpPort:           int32(fs.FtpPort()),
		SftpPort:          int32(fs.SftpPort()),
		MountPoint:        cs.MountPoint(),
		Mounts:            cs.Mounts().ToRPC(),
		MechanismArgsDesc: generateMechanismDescription(ii.Spec),
		Environment:       cs.Env(),
	}
}

func (fs *fwdState) HandlePort(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	clog.Debugf(ctx, "fwdState.HandlePort %d called with %d intercepts", fs.intercept.ContainerPort(), len(cepts))

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

	fwd := fs.forwarder
	if fs.sessionInfo != nil {
		// Update forwarding.
		fwd.SetStreamProvider(fs)
	}

	// Check if we have HTTP intercepts (any with HeaderFilters or PathFilters)
	var intercepts, wiretaps []*manager.InterceptInfo
	for _, is := range active {
		if is.Spec.Wiretap {
			wiretaps = append(wiretaps, is)
		} else {
			intercepts = append(intercepts, is)
		}
	}
	fwd.SetWiretapping(wiretaps)
	fwd.SetIntercepting(intercepts)

	// Review waiting intercepts
	reviews := make([]*manager.ReviewInterceptRequest, 0, len(waiting))

	// Collect all intercepts that could potentially become active
	candidateIntercepts := slices.Clone(waiting)

	for i, ii := range candidateIntercepts {
		if ii.Spec.Wiretap {
			reviews = append(reviews, fs.processWiretapIntercept(ii))
			continue
		}

		// Check for conflicts and process regular intercept
		review := fs.processRegularIntercept(ctx, ii, i, active, candidateIntercepts)
		reviews = append(reviews, review)
	}
	return reviews
}
