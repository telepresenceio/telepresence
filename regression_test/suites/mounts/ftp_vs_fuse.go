package mounts

import (
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// FTPvsFUSE proves the mounted ConfigMap content is identical whether the
// daemon serves the mount over SFTP (intercept.useFtp: true) or the default,
// FUSE (intercept.useFtp: false) -- pkg/client/config.go's
// Intercept.UseFtp, json tag "useFtp".
type FTPvsFUSE struct {
	rt.Suite
}

func init() {
	rt.Register(&FTPvsFUSE{}, rt.InArea("mounts"), rt.NeedsManager(managers.Default),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// enableFtp and disableFtp are ConnWithConfig deltas for
// Test_ContentUnderFTPAndFUSE's config-variant connection pair; both are
// explicit so neither connection depends on the run's baseline default (see
// suites/intercept/routing.go's enableLocalShortcut/disableLocalShortcut for
// the same convention).
func enableFtp(c client.Config) {
	c.Intercept().UseFtp = true
}

func disableFtp(c client.Config) {
	c.Intercept().UseFtp = false
}

// Test_ContentUnderFTPAndFUSE checks the mounted content under both
// intercept.useFtp variants, one connection at a time: both share the
// host's single daemon slot, so provisioning the second quits and restarts
// under the other config dir (fixture_connection.go's ensureHostDaemon),
// mirroring suites/intercept/routing.go's Test_LocalShortcut.
func (s *FTPvsFUSE) Test_ContentUnderFTPAndFUSE() {
	t := s.T()
	s.Manager()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.EchoWithConfigVolume("mounts-ftp-vs-fuse"))

	ftp := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(enableFtp)))
	a := ftp.Intercept(t, wl)
	rootFtp, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	check.EventuallyFile(t, configFilePath(rootFtp), isConfigContent, mountTimeout)
	a.Detach(t)

	fuse := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableFtp)))
	b := fuse.Intercept(t, wl)
	defer b.Detach(t)
	rootFuse, ok := rt.MountRoot(b)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	check.EventuallyFile(t, configFilePath(rootFuse), isConfigContent, mountTimeout)
}
