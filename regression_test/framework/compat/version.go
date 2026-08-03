// Package compat holds the regression-test framework's compat-core support:
// effective-version accessors, version gates, and the RPC manifest guard
// (manifest.go/manifest_test.go) that keeps the compat-core test set honest
// about which manager.Manager RPCs it exercises.
package compat

import (
	"testing"

	"github.com/blang/semver/v4"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// ClientVersion returns the effective semver of the client binary under
// test: RTEST_CLIENT_VERSION when set, otherwise the built binary's own
// version (rt.Runtime.Version).
func ClientVersion() semver.Version { return rt.R().Version() }

// ManagerVersion returns the effective semver of the traffic-manager
// chart/images under test: RTEST_MANAGER_VERSION when set, otherwise the
// built binary's own version (rt.Runtime.ManagerVersion).
func ManagerVersion() semver.Version { return rt.R().ManagerVersion() }

// isFinalIncluded reports whether v's finalized version (pre-release and
// build metadata stripped) satisfies the blang semver range vr.
func isFinalIncluded(vr string, v semver.Version) bool {
	return semver.MustParseRange(vr)(semver.MustParse(v.FinalizeVersion()))
}

// MinManager skips t unless the effective manager version satisfies the
// blang semver range constraint (e.g. ">=2.30.0", ">2.29.x"). Mirrors
// itest's ManagerIsVersion gate; a compat-core test guards a feature the
// old side of a compat run might lack by calling this before exercising it.
func MinManager(t testing.TB, constraint string) {
	t.Helper()
	v := ManagerVersion()
	if !isFinalIncluded(constraint, v) {
		t.Skipf("requires traffic-manager %s, got %s", constraint, v)
	}
}

// MinClient skips t unless the effective client version satisfies the
// blang semver range constraint. Mirrors itest's ClientIsVersion gate.
func MinClient(t testing.TB, constraint string) {
	t.Helper()
	v := ClientVersion()
	if !isFinalIncluded(constraint, v) {
		t.Skipf("requires client %s, got %s", constraint, v)
	}
}
