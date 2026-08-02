package mounts

import (
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Ignored proves the telepresence.io/inject-ignore-volume-mounts annotation
// (pkg/annotation/annotation.go's InjectIgnoreVolumeMounts) excludes a named
// volume from both TELEPRESENCE_MOUNTS (the attach's reported environment)
// and the local mount, trimmed from integration_test/ignored_mounts_test.go's
// Test_IgnoredMounts 4-case table to the two cases that isolate the
// annotation's own effect: ignored and not-ignored.
type Ignored struct {
	rt.Suite
}

func init() {
	rt.Register(&Ignored{}, rt.InArea("mounts"), rt.NeedsManager(managers.Default),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// Test_NotIgnored intercepts an unannotated EchoWithConfigVolume: the
// ConfigMap's mount path is present in TELEPRESENCE_MOUNTS and its content is
// readable through the mount.
func (s *Ignored) Test_NotIgnored() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoWithConfigVolume("mounts-not-ignored"))

	a := conn.Intercept(t, wl)
	defer a.Detach(t)

	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	// A guaranteed mount confirms the FUSE/SFTP layer is fully up before
	// checking the ConfigVolume-specific paths below.
	check.EventuallyFile(t, tokenFilePath(root), isNonEmpty, mountTimeout)

	paths := mountedPaths(a)
	s.True(containsPath(paths, workloads.ConfigVolumeMountPath),
		"TELEPRESENCE_MOUNTS %q should contain %s", paths, workloads.ConfigVolumeMountPath)

	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountTimeout)
}

// Test_Ignored intercepts an EchoWithConfigVolume annotated to ignore its
// ConfigMap volume (by Kubernetes Volume name, configVolumeK8sName): the
// ConfigMap's mount path is absent from TELEPRESENCE_MOUNTS and never
// appears under the local mount.
func (s *Ignored) Test_Ignored() {
	t := s.T()
	conn := s.Connect()
	tpl := workloads.EchoWithConfigVolume("mounts-ignored")
	tpl.Annotations = map[string]string{annotation.InjectIgnoreVolumeMounts: configVolumeK8sName}
	wl := s.Workload(tpl)

	a := conn.Intercept(t, wl)
	defer a.Detach(t)

	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	// A guaranteed mount confirms the FUSE/SFTP layer is fully up, so the
	// immediate stat below reliably distinguishes "not mounted" from "not
	// wired up yet".
	check.EventuallyFile(t, tokenFilePath(root), isNonEmpty, mountTimeout)

	paths := mountedPaths(a)
	s.False(containsPath(paths, workloads.ConfigVolumeMountPath),
		"TELEPRESENCE_MOUNTS %q should not contain ignored %s", paths, workloads.ConfigVolumeMountPath)

	_, err := os.Stat(configFilePath(root))
	s.Error(err, "ignored ConfigMap path should not appear under the mount")
}
