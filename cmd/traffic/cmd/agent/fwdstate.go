package agent

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
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

// generateMechanismDescription creates a human-readable description for the intercept mechanism.
func generateMechanismDescription(spec *manager.InterceptSpec) string {
	if spec.Mechanism != "http" {
		return "all TCP connections"
	}

	// Build HTTP filter description
	var filters []string

	// Add header filters
	for key, value := range spec.HeaderFilters {
		filters = append(filters, fmt.Sprintf("header %s=%s", key, value))
	}

	// Add path filters
	for _, path := range spec.PathFilters {
		filters = append(filters, fmt.Sprintf("path %s", path))
	}

	if len(filters) > 0 {
		return fmt.Sprintf("HTTP filters: %s", strings.Join(filters, ", "))
	}

	return "all HTTP connections"
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

// interceptSpecsConflict determines if two intercept specs would conflict with each other.
// Two specs conflict if they would route the same traffic to different destinations.
//
// Precedence Model:
// Headers take precedence over paths. This means intercepts with headers operate at a higher
// priority tier than intercepts with only paths.
//
// Conflict Rules:
// 1. Global intercepts (no headers, no paths) conflict with everything
// 2. Headers vs Paths: One spec with headers, another with only paths → NO CONFLICT
//    (different priority tiers - headers are checked first, then paths)
// 3. Both have headers: Conflict if headers form a subset AND paths overlap
//    - Within each intercept, filters use AND logic (must match ALL headers AND ALL paths)
//    - Example: {x-user:adam} vs {x-user:adam, x-session:xyz} → CONFLICT (first is subset)
//    - Example: {x-user:adam}+/api/* vs {x-user:adam}+/admin/* → NO CONFLICT (different paths)
// 4. Both have only paths (no headers): Conflict if paths overlap
func interceptSpecsConflict(spec1, spec2 *manager.InterceptSpec) bool {
	hasHeaders1 := len(spec1.HeaderFilters) > 0
	hasHeaders2 := len(spec2.HeaderFilters) > 0
	hasPaths1 := len(spec1.PathFilters) > 0
	hasPaths2 := len(spec2.PathFilters) > 0

	// Global intercept: no headers and no paths means it intercepts everything
	isGlobal1 := !hasHeaders1 && !hasPaths1
	isGlobal2 := !hasHeaders2 && !hasPaths2

	// Rule 1: Global intercepts conflict with anything
	if isGlobal1 || isGlobal2 {
		return true
	}

	// Rule 2: Headers take precedence - different priority tiers don't conflict
	// If one spec has headers and the other has only paths, they operate at different tiers
	if hasHeaders1 && !hasHeaders2 && !isGlobal2 {
		// spec1 has headers (high priority), spec2 has only paths (low priority)
		return false
	}
	if hasHeaders2 && !hasHeaders1 && !isGlobal1 {
		// spec2 has headers (high priority), spec1 has only paths (low priority)
		return false
	}

	// Helper function to check if all headers in subset exist in superset with same values
	isHeaderSubset := func(subset, superset map[string]string) bool {
		for key, value := range subset {
			if superValue, exists := superset[key]; !exists || superValue != value {
				return false
			}
		}
		return true
	}

	// Rule 3: Both have headers - check for subset relationship AND path overlap
	if hasHeaders1 && hasHeaders2 {
		// Check if headers form a subset relationship
		hasHeaderSubset := isHeaderSubset(spec1.HeaderFilters, spec2.HeaderFilters) ||
			isHeaderSubset(spec2.HeaderFilters, spec1.HeaderFilters)

		if !hasHeaderSubset {
			// Headers don't form subset, no conflict
			return false
		}

		// Headers form subset, now check paths
		if !hasPaths1 || !hasPaths2 {
			// At least one has no path restriction, so paths overlap
			return true
		}

		// Both have paths: check for overlap
		for _, path1 := range spec1.PathFilters {
			if slices.Contains(spec2.PathFilters, path1) {
				return true
			}
		}
		// Different paths, no conflict
		return false
	}

	// Rule 4: Both have only paths (no headers) - check for path overlap
	if hasPaths1 && hasPaths2 {
		for _, path1 := range spec1.PathFilters {
			if slices.Contains(spec2.PathFilters, path1) {
				return true
			}
		}
		return false
	}

	// Should not reach here, but default to no conflict
	return false
}

// processWiretapIntercept handles wiretap intercepts which can always be active alongside others.
func (fs *fwdState) processWiretapIntercept(ii *manager.InterceptInfo) *manager.ReviewInterceptRequest {
	container := ii.Spec.ContainerName
	if container == "" {
		container = fs.container
	}
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
		if !activeII.Spec.Wiretap && interceptSpecsConflict(ii.Spec, activeII.Spec) {
			return activeII
		}
	}

	// Check for conflicts with other waiting intercepts that would become active
	// Only check intercepts that come before this one (first wins policy)
	for j, otherII := range candidates {
		if j < index && !otherII.Spec.Wiretap && interceptSpecsConflict(ii.Spec, otherII.Spec) {
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
	activeIntercept **manager.InterceptInfo,
) *manager.ReviewInterceptRequest {
	conflictingIntercept := fs.findConflictingIntercept(ii, index, active, candidates)

	if conflictingIntercept != nil {
		// Reject due to actual conflict
		chosenID := conflictingIntercept.Id
		dlog.Infof(ctx, "Setting intercept %q as AGENT_ERROR; as it conflicts with %q", ii.Id, chosenID)
		var msg string
		if conflictingIntercept.Disposition == manager.InterceptDispositionType_ACTIVE {
			msg = fmt.Sprintf("Conflicts with the currently-served intercept %q", chosenID)
		} else {
			msg = fmt.Sprintf("Conflicts with the currently-waiting-to-be-served intercept %q", chosenID)
		}
		return &manager.ReviewInterceptRequest{
			Id:                ii.Id,
			Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
			Message:           msg,
			MechanismArgsDesc: generateMechanismDescription(ii.Spec),
		}
	}

	// No conflict detected, allow this intercept to become active
	container := ii.Spec.ContainerName
	if container == "" {
		container = fs.container
	}
	cs := fs.containerStates[container]
	if cs == nil {
		return &manager.ReviewInterceptRequest{
			Id:                ii.Id,
			Disposition:       manager.InterceptDispositionType_AGENT_ERROR,
			Message:           fmt.Sprintf("No match for container %q", container),
			MechanismArgsDesc: generateMechanismDescription(ii.Spec),
		}
	}
	// Only set activeIntercept for global/TCP intercepts (no filters)
	// HTTP intercepts with filters use the multiple-intercept mode instead
	isGlobalIntercept := len(ii.Spec.HeaderFilters) == 0 && len(ii.Spec.PathFilters) == 0
	if !ii.Spec.Wiretap && isGlobalIntercept && *activeIntercept == nil {
		// Set the first global non-wiretap intercept as the active one for the forwarder
		*activeIntercept = ii
	}
	dlog.Infof(ctx, "Allowing non-conflicting intercept %q to become active", ii.Id)
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
				// Only track global/TCP intercepts as activeIntercept
				isGlobalIntercept := len(is.Spec.HeaderFilters) == 0 && len(is.Spec.PathFilters) == 0
				if !is.Spec.Wiretap && isGlobalIntercept {
					activeIntercept = is
				}
				break
			}
		}
	}

	if activeIntercept == nil {
		fs.chosenInterceptId = ""

		// Attach to already ACTIVE global/TCP intercept if there is one.
		for _, is := range active {
			isGlobalIntercept := len(is.Spec.HeaderFilters) == 0 && len(is.Spec.PathFilters) == 0
			if !is.Spec.Wiretap && isGlobalIntercept {
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

	// Check if we have HTTP intercepts (any with HeaderFilters or PathFilters)
	var httpIntercepts []*manager.InterceptInfo
	for _, is := range active {
		if !is.Spec.Wiretap {
			spec := is.Spec
			if len(spec.HeaderFilters) > 0 || len(spec.PathFilters) > 0 {
				httpIntercepts = append(httpIntercepts, is)
			}
		}
	}

	if len(httpIntercepts) > 0 {
		// We have HTTP intercepts - use multiple intercept mode
		dlog.Debugf(ctx, "Setting %d HTTP intercepts on forwarder", len(httpIntercepts))
		fwd.SetInterceptingMultiple(ctx, httpIntercepts)
	} else {
		// No HTTP filters - use single intercept mode for TCP
		fwd.SetIntercepting(ctx, activeIntercept)
	}

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

	// Collect all intercepts that could potentially become active
	candidateIntercepts := slices.Clone(waiting)

	for i, ii := range candidateIntercepts {
		if ii.Spec.Wiretap {
			reviews = append(reviews, fs.processWiretapIntercept(ii))
			continue
		}

		// Check for conflicts and process regular intercept
		review := fs.processRegularIntercept(ctx, ii, i, active, candidateIntercepts, &activeIntercept)
		reviews = append(reviews, review)
	}
	return reviews
}
