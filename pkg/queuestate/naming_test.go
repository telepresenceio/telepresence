package queuestate

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"
)

// charsetRE matches the general bounded-name charset: lowercase alphanumeric,
// '.', and '-', starting and ending with an alphanumeric character.
var charsetRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

const (
	testInstallID    = "install-abc"
	testWorkloadUID  = "8f14e45f-ceea-467e-9dd6-1e9a0b7b6d0f"
	testLogicalQueue = "orders"
	testActivationID = "act-1"
	testRouteID      = "route-1"
)

// queueScopedNamers are the naming functions keyed by (installID, workloadUID,
// logicalQueue, activationID), with no route ID.
var queueScopedNamers = map[string]func(installID, workloadUID, logicalQueue, activationID string) string{
	"AppShadowName":     AppShadowName,
	"AppGroupName":      AppGroupName,
	"SplitterGroupName": SplitterGroupName,
	"LockQueueName":     LockQueueName,
}

// routeScopedNamers are the naming functions keyed by (installID, workloadUID,
// logicalQueue, activationID, routeID).
var routeScopedNamers = map[string]func(installID, workloadUID, logicalQueue, activationID, routeID string) string{
	"SessionShadowName": SessionShadowName,
	"SessionGroupName":  SessionGroupName,
	"DrainGroupName":    DrainGroupName,
}

// workloadScopedNamers are the naming functions keyed by (installID,
// workloadUID) only.
var workloadScopedNamers = map[string]func(installID, workloadUID string) string{
	"StateConfigMapName":  StateConfigMapName,
	"AgentDeploymentName": AgentDeploymentName,
}

func allTestNames() map[string]string {
	names := make(map[string]string)
	for n, f := range queueScopedNamers {
		names[n] = f(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID)
	}
	for n, f := range routeScopedNamers {
		names[n] = f(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID, testRouteID)
	}
	for n, f := range workloadScopedNamers {
		names[n] = f(testInstallID, testWorkloadUID)
	}
	return names
}

func TestNamingDeterministic(t *testing.T) {
	first := allTestNames()
	second := allTestNames()
	assert.Equal(t, first, second)
}

func TestNamingDistinctAcrossFunctions(t *testing.T) {
	names := allTestNames()
	seen := make(map[string]string, len(names))
	for fn, name := range names {
		if other, ok := seen[name]; ok {
			t.Fatalf("%s and %s both produced %q for identical inputs", fn, other, name)
		}
		seen[name] = fn
	}
}

func TestNamingDistinctAcrossDifferingInputs(t *testing.T) {
	base := AppShadowName(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID)
	assert.NotEqual(t, base, AppShadowName("other-install", testWorkloadUID, testLogicalQueue, testActivationID))
	assert.NotEqual(t, base, AppShadowName(testInstallID, "other-uid", testLogicalQueue, testActivationID))
	assert.NotEqual(t, base, AppShadowName(testInstallID, testWorkloadUID, "invoices", testActivationID))
	assert.NotEqual(t, base, AppShadowName(testInstallID, testWorkloadUID, testLogicalQueue, "act-2"))
}

func TestNamingDistinctAcrossRouteID(t *testing.T) {
	a := SessionShadowName(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID, "route-1")
	b := SessionShadowName(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID, "route-2")
	assert.NotEqual(t, a, b)

	g1 := SessionGroupName(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID, "route-1")
	g2 := SessionGroupName(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID, "route-2")
	assert.NotEqual(t, g1, g2)

	d1 := DrainGroupName(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID, "route-1")
	d2 := DrainGroupName(testInstallID, testWorkloadUID, testLogicalQueue, testActivationID, "route-2")
	assert.NotEqual(t, d1, d2)
}

func TestNamingLengthBoundWithAbsurdlyLongInputs(t *testing.T) {
	long := strings.Repeat("x", 10_000)
	unicodeLong := strings.Repeat("é-topic_", 2_000) // accented + separators + underscores

	for _, in := range []string{long, unicodeLong} {
		for fn, f := range queueScopedNamers {
			name := f(in, in, in, in)
			assert.LessOrEqualf(t, len(name), maxNameLen, "%s length", fn)
		}
		for fn, f := range routeScopedNamers {
			name := f(in, in, in, in, in)
			assert.LessOrEqualf(t, len(name), maxNameLen, "%s length", fn)
		}
		for fn, f := range workloadScopedNamers {
			name := f(in, in)
			assert.LessOrEqualf(t, len(name), maxNameLen, "%s length", fn)
		}
	}
}

func TestNamingCharsetValidity(t *testing.T) {
	inputs := []string{
		testLogicalQueue,
		"Orders With Spaces",
		"weird/topic:name!!",
		"UPPER_CASE",
		"",
		strings.Repeat("z", 500),
		"unicode-éß中",
	}
	for _, in := range inputs {
		for fn, f := range queueScopedNamers {
			name := f(testInstallID, testWorkloadUID, in, testActivationID)
			assert.Regexpf(t, charsetRE, name, "%s(%q)", fn, in)
			assert.LessOrEqual(t, len(name), maxNameLen)
		}
		for fn, f := range routeScopedNamers {
			name := f(testInstallID, testWorkloadUID, in, testActivationID, testRouteID)
			assert.Regexpf(t, charsetRE, name, "%s(%q)", fn, in)
			assert.LessOrEqual(t, len(name), maxNameLen)
		}
	}
	for fn, f := range workloadScopedNamers {
		name := f(testInstallID, testWorkloadUID)
		assert.Regexpf(t, charsetRE, name, fn)
	}
}

func TestNamingDoesNotEmbedTopicVerbatim(t *testing.T) {
	secretQueue := "super-secret-topic-name-that-must-not-leak-verbatim"
	name := AppShadowName(testInstallID, testWorkloadUID, secretQueue, testActivationID)
	assert.NotContains(t, name, secretQueue)
}

func TestNamingKubernetesNamesAreValidDNS1123Subdomains(t *testing.T) {
	for fn, f := range workloadScopedNamers {
		name := f(testInstallID, testWorkloadUID)
		errs := validation.IsDNS1123Subdomain(name)
		assert.Emptyf(t, errs, "%s produced %q: %v", fn, name, errs)
	}

	// Also valid for absurdly long inputs.
	long := strings.Repeat("y", 5_000)
	for fn, f := range workloadScopedNamers {
		name := f(long, long)
		errs := validation.IsDNS1123Subdomain(name)
		assert.Emptyf(t, errs, "%s produced %q: %v", fn, name, errs)
	}
}

func TestNamingBuildNameHelperOmitsHumanSegmentWhenNoRoom(t *testing.T) {
	// A prefix so long that no room remains for a human-readable segment
	// still produces a valid, bounded, non-empty name.
	name := buildName(strings.Repeat("p", maxNameLen-hashHexLen), "kind", "queue", "a", "b")
	require.NotEmpty(t, name)
	assert.LessOrEqual(t, len(name), maxNameLen)
	assert.Regexp(t, charsetRE, name)
}

func TestSanitizeSegmentCollapsesAndTrims(t *testing.T) {
	assert.Equal(t, "a-b-c", sanitizeSegment("A///b   c--"))
	assert.Equal(t, "", sanitizeSegment("---"))
	assert.Equal(t, "abc", sanitizeSegment("abc"))
}
