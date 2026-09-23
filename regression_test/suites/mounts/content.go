package mounts

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Content proves a mounts-enabled intercept (no --mount false) exposes the
// ConfigMap content workloads.EchoWithConfigVolume mounts, and that the
// serviceaccount token -- present on any pod -- is readable through the same
// mount.
//
// A write round-trip needs a PersistentVolumeClaim-backed writable volume: a
// separate PV/PVC plus its own "hello" Deployment, not anything
// workloads.EchoWithConfigVolume mounts (its only extra volume is a
// read-only ConfigMap). Reproducing that PVC setup is out of this suite's
// scope (EchoWithConfigVolume + its exported constants only), so the write
// round-trip is skipped; the guaranteed-present, always-read-only
// serviceaccount token file stands in for "is this mount actually usable".
type Content struct {
	rt.Suite
}

func init() {
	rt.Register(&Content{}, rt.InArea("mounts"), rt.NeedsManager(managers.Default),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// Test_ConfigMapContent intercepts EchoWithConfigVolume with mounts enabled
// (the default; no --mount false) and asserts the mounted ConfigMap file's
// content matches, and the serviceaccount token file is readable. It also
// asserts that the generated mount point directory (the user daemon's
// telfs-* temporary directory, since no --mount path was given) is gone once
// the intercept ends: the user daemon, not the CLI, owns that directory and
// removes it when the intercept is torn down.
func (s *Content) Test_ConfigMapContent() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoWithConfigVolume("mounts-content"))

	a := conn.Intercept(t, wl)
	defer a.Detach(t)

	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	t.Cleanup(func() {
		// Runs after the deferred Detach above, so the intercept has
		// already ended by the time this checks for the directory.
		check.EventuallyRemoved(t, root, mountTimeout)
	})

	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountTimeout)
	check.EventuallyFile(t, tokenFilePath(root), isNonEmpty, mountTimeout)
}
