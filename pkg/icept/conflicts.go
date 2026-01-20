package icept

import (
	"regexp"
	"slices"
	"strings"

	"k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// pathFiltersOverlap checks if two sets of path filters can match the same request path.
// This properly handles the three path filter types: :path-equal:, :path-prefix:, and :path-regex:.
func pathFiltersOverlap(paths1, paths2 []string) bool {
	for _, path1 := range paths1 {
		for _, path2 := range paths2 {
			if pathsCanMatch(path1, path2) {
				return true
			}
		}
	}
	return false
}

// pathsCanMatch determines if two path filters can match the same request path.
func pathsCanMatch(filter1, filter2 string) bool {
	return valuesCanMatch(matcher.PathValue(filter1), matcher.PathValue(filter2))
}

// valuesCanMatch determines if two matcher.Value objects could potentially match the same input.
// It is used to detect conflicts between intercept path filters.
// Supported operations: Equal, Prefix, Regex.
//
// The logic ensures that overlapping or equivalent patterns (e.g., /api/* vs /api/v1/*)
// are treated as conflicts, while unrelated ones (e.g., /api/v1/* vs /api/v2/*) are not.
func valuesCanMatch(v1, v2 matcher.Value) bool {
	op1 := v1.Op()
	op2 := v2.Op()
	pattern1 := v1.String()
	pattern2 := v2.String()

	switch {
	case op1 == matcher.ValueOpEqual && op2 == matcher.ValueOpEqual:
		// Both exact matches - conflict only if the same value
		return pattern1 == pattern2

	case op1 == matcher.ValueOpPrefix && op2 == matcher.ValueOpPrefix:
		// Both are prefixes - conflict if one is a prefix of the other
		return strings.HasPrefix(pattern1, pattern2) || strings.HasPrefix(pattern2, pattern1)

	case op1 == matcher.ValueOpPrefix && op2 == matcher.ValueOpEqual:
		// Prefix can match an exact value if the value starts with the prefix
		return strings.HasPrefix(pattern2, pattern1)

	case op1 == matcher.ValueOpEqual && op2 == matcher.ValueOpPrefix:
		// Exact value matches prefix if it starts with the prefix
		return strings.HasPrefix(pattern1, pattern2)

	case op1 == matcher.ValueOpRegex && op2 == matcher.ValueOpRegex:
		// --- Regex vs Regex ---
		if pattern1 == pattern2 {
			return true
		}

		anchored1 := strings.HasPrefix(pattern1, "^")
		anchored2 := strings.HasPrefix(pattern2, "^")

		prefix1 := regexLiteralPrefix(pattern1)
		prefix2 := regexLiteralPrefix(pattern2)

		// Both anchored → only overlap if one is a prefix of the other
		if anchored1 && anchored2 {
			return strings.HasPrefix(prefix1, prefix2) || strings.HasPrefix(prefix2, prefix1)
		}
		// Fallback conservative
		return true

	case op1 == matcher.ValueOpRegex && (op2 == matcher.ValueOpEqual || op2 == matcher.ValueOpPrefix):
		return regexCanMatchValue(pattern1, op2, pattern2)

	case op2 == matcher.ValueOpRegex && (op1 == matcher.ValueOpEqual || op1 == matcher.ValueOpPrefix):
		return regexCanMatchValue(pattern2, op1, pattern1)
	}

	// Fallback: conservatively assume a potential match
	return true
}

// regexCanMatchValue checks if a regex could match an equal or prefix value.
// Conservative: returns true if regex is invalid or overlap can't be ruled out.
func regexCanMatchValue(regexPattern string, op matcher.ValueOp, value string) bool {
	if value == "" {
		return true
	}

	anchored := strings.HasPrefix(regexPattern, "^")

	switch op {
	case matcher.ValueOpEqual:
		if anchored {
			re, err := regexp.Compile(regexPattern)
			if err != nil {
				return true // invalid regex → assume conflict
			}
			return re.MatchString(value)
		}
		// Unanchored regex → can match anywhere in the string
		return true

	case matcher.ValueOpPrefix:
		if anchored {
			// Anchored regex → behaves like HasPrefix using literal prefix
			prefix := regexLiteralPrefix(regexPattern)
			if prefix == "" {
				return true
			}
			return strings.HasPrefix(value, prefix) || strings.HasPrefix(prefix, value)
		}

		// Unanchored regex → can match anywhere in the string
		return true
	}
	// fallback
	return true
}

// regexLiteralPrefix safely extracts a literal prefix from a regex pattern.
// Stops at first regex metacharacter. Conservative for complex regexes.
func regexLiteralPrefix(pattern string) string {
	if pattern == "" {
		return ""
	}

	// Remove the leading '^' (safe: TrimPrefix is a no-op if not present)
	pattern = strings.TrimPrefix(pattern, "^")

	// Remove trailing '.*' (safe: TrimSuffix is a no-op if not present)
	pattern = strings.TrimSuffix(pattern, ".*")

	var prefix strings.Builder
	for _, r := range pattern {
		// Stop at any regex metacharacter
		if strings.ContainsRune("[](){}?+*|$.\\^", r) {
			break
		}
		prefix.WriteRune(r)
	}

	return prefix.String()
}

// ExplainConflict generates a human-readable explanation of why two intercept specs conflict.
func ExplainConflict(spec1, spec2 *manager.InterceptSpec) string {
	hasHeaders1 := len(spec1.HeaderFilters) > 0
	hasHeaders2 := len(spec2.HeaderFilters) > 0
	hasPaths1 := len(spec1.PathFilters) > 0
	hasPaths2 := len(spec2.PathFilters) > 0

	isGlobal1 := !hasHeaders1 && !hasPaths1
	isGlobal2 := !hasHeaders2 && !hasPaths2

	if isGlobal1 || isGlobal2 {
		return "one intercept has no filters (intercepts all traffic)"
	}

	if hasHeaders1 && hasHeaders2 {
		// Normalize headers for comparison
		headers1 := matcher.NewHeaders(spec1.HeaderFilters)
		headers2 := matcher.NewHeaders(spec2.HeaderFilters)

		// Check if headers form a subset relationship
		isSubset1of2 := isHeaderSubset(headers1, headers2)
		isSubset2of1 := isHeaderSubset(headers2, headers1)

		if isSubset1of2 || isSubset2of1 {
			if !hasPaths1 || !hasPaths2 || pathFiltersOverlap(spec1.PathFilters, spec2.PathFilters) {
				subsetDesc := "header filters"
				if hasPaths1 && hasPaths2 {
					subsetDesc += " and paths"
				}
				return subsetDesc + " overlap"
			}
		}
	}

	if hasPaths1 && hasPaths2 && !hasHeaders1 && !hasHeaders2 {
		return "path filters overlap"
	}

	return "filters would route the same traffic to different destinations"
}

// isHeaderSubset checks if all headers in a subset exist in superset with matching values.
// Uses wildcard matching for header values.
func isHeaderSubset(subset, superset matcher.Headers) bool {
	supersetMap := superset.HeaderMap()
	for key, value := range subset.HeaderMap() {
		if superValue, exists := supersetMap[key]; !exists || !valuesCanMatch(value, superValue) {
			return false
		}
	}
	return true
}

// IsInConflict determines if two intercept specs would conflict with each other. It's assumed
// that `spec2` has been determined to be a potential conflict with `spec1` already with respect
// to the agent and the targeted ports.
//
// Two specs conflict if they route the same traffic to different destinations.
//
// Precedence Model:
// Headers take precedence over paths. This means intercepts with headers operate at a higher
// priority tier than intercepts with only paths.
//
// Conflict Rules:
//  1. Global intercepts (no headers, no paths) conflict with everything
//  2. Headers vs. Paths: One spec with headers, another with only paths → NO CONFLICT
//     (different priority tiers - headers are checked first, then paths)
//  3. Both have headers: Conflict if headers form a subset AND paths overlap
//     - Within each intercept, filters use AND logic (must match ALL headers AND ALL paths)
//     - Example: {x-user:adam} vs. {x-user:adam, x-session:xyz} → CONFLICT (first is subset)
//     - Example: {x-user:adam}+/api/* vs {x-user:adam}+/admin/* → NO CONFLICT (different paths)
//  4. Both have only paths (no headers): Conflict if paths overlap
func IsInConflict(spec1, spec2 *manager.InterceptSpec) bool {
	hasHeaders1 := len(spec1.HeaderFilters) > 0
	hasHeaders2 := len(spec2.HeaderFilters) > 0
	hasPaths1 := len(spec1.PathFilters) > 0
	hasPaths2 := len(spec2.PathFilters) > 0

	// Global intercept: no headers and no paths will intercept everything
	isGlobal1 := !hasHeaders1 && !hasPaths1
	isGlobal2 := !hasHeaders2 && !hasPaths2

	// Rule 2: Headers take precedence - different priority tiers don't conflict
	// If one spec has headers and the other has only paths, they operate at different tiers
	switch {
	case isGlobal1 || isGlobal2:
		// Rule 1: Global intercepts conflict with anything
		return true
	case hasHeaders1 && !hasHeaders2:
		// spec1 has headers (high priority), spec2 has only paths (low priority)
		return false
	case hasHeaders2 && !hasHeaders1:
		// spec2 has headers (high priority), spec1 has only paths (low priority)
		return false
	case hasHeaders1:
		// Rule 3: Both have headers - check for subset relationship AND path overlap
		// Normalize headers for case-insensitive comparison (HTTP headers per RFC 7230)
		headers1 := matcher.NewHeaders(spec1.HeaderFilters)
		headers2 := matcher.NewHeaders(spec2.HeaderFilters)

		// Check if headers form a subset relationship
		hasHeaderSubset := isHeaderSubset(headers1, headers2) || isHeaderSubset(headers2, headers1)
		if !hasHeaderSubset {
			// Headers don't form a subset, no conflict
			return false
		}

		// Headers form subset, now check paths
		if !hasPaths1 || !hasPaths2 {
			// At least one has no path restriction, so paths overlap
			return true
		}

		fallthrough
	default:
		// Rule 4: Both have only paths (no headers) - check for path overlap
		return pathFiltersOverlap(spec1.PathFilters, spec2.PathFilters)
	}
}

// PotentialConflict returns true if the given intercept spec may be in conflict with another intercept affecting the
// given agent and ports.
func PotentialConflict(agentName, namespace string, containerPorts []types.PortAndProto, info *manager.InterceptInfo) bool {
	switch info.Disposition {
	case manager.InterceptDispositionType_ACTIVE, manager.InterceptDispositionType_WAITING, manager.InterceptDispositionType_NO_AGENT:
		spec := info.Spec
		if !spec.Wiretap && spec.Agent == agentName && spec.Namespace == namespace {
			pp := types.PortAndProto{Port: uint16(spec.ContainerPort), Proto: types.FromK8sProtocol(v1.Protocol(spec.Protocol))}
			if slices.Contains(containerPorts, pp) {
				return true
			}
		}
	}
	return false
}
