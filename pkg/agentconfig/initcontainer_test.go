package agentconfig

import (
	"testing"

	core "k8s.io/api/core/v1"
)

func envValue(ic *core.Container, name string) (string, bool) {
	for _, env := range ic.Env {
		if env.Name == name {
			return env.Value, true
		}
	}
	return "", false
}

func volumeMount(ic *core.Container, name string) (core.VolumeMount, bool) {
	for _, vm := range ic.VolumeMounts {
		if vm.Name == name {
			return vm, true
		}
	}
	return core.VolumeMount{}, false
}

func TestInitContainerSetsAgentUIDAndGID(t *testing.T) {
	uid := int64(1000)
	gid := int64(7439)
	ic := InitContainer(&Sidecar{AgentImage: "traffic-agent"}, &core.SecurityContext{RunAsUser: &uid, RunAsGroup: &gid}, "")

	if v, ok := envValue(ic, EnvAgentUID); !ok || v != "1000" {
		t.Fatalf("%s = %q, want 1000", EnvAgentUID, v)
	}
	if v, ok := envValue(ic, EnvAgentGID); !ok || v != "7439" {
		t.Fatalf("%s = %q, want 7439", EnvAgentGID, v)
	}
}

func TestInitContainerSetsCoverDir(t *testing.T) {
	ic := InitContainer(&Sidecar{AgentImage: "traffic-agent"}, nil, "/rtest-coverage")

	if v, ok := envValue(ic, "GOCOVERDIR"); !ok || v != "/rtest-coverage" {
		t.Fatalf("GOCOVERDIR = %q, want /rtest-coverage", v)
	}
	vm, ok := volumeMount(ic, CoverVolumeName)
	if !ok || vm.MountPath != "/rtest-coverage" {
		t.Fatalf("%s mount = %+v, want MountPath /rtest-coverage", CoverVolumeName, vm)
	}
}

func TestInitContainerOmitsCoverDirWhenUnset(t *testing.T) {
	ic := InitContainer(&Sidecar{AgentImage: "traffic-agent"}, nil, "")

	if v, ok := envValue(ic, "GOCOVERDIR"); ok {
		t.Fatalf("GOCOVERDIR = %q, want unset", v)
	}
	if _, ok := volumeMount(ic, CoverVolumeName); ok {
		t.Fatalf("%s mount present, want absent", CoverVolumeName)
	}
}

func TestInitContainerOmitsAgentUIDWhenUnknown(t *testing.T) {
	ic := InitContainer(&Sidecar{AgentImage: "traffic-agent"}, nil, "")

	if v, ok := envValue(ic, EnvAgentUID); ok {
		t.Fatalf("%s = %q, want unset", EnvAgentUID, v)
	}
	if v, ok := envValue(ic, EnvAgentGID); ok {
		t.Fatalf("%s = %q, want unset", EnvAgentGID, v)
	}
}

func TestInitContainerOmitsAgentUIDWhenOnlyGroupIsKnown(t *testing.T) {
	gid := int64(7439)
	ic := InitContainer(&Sidecar{AgentImage: "traffic-agent"}, &core.SecurityContext{RunAsGroup: &gid}, "")

	if v, ok := envValue(ic, EnvAgentUID); ok {
		t.Fatalf("%s = %q, want unset", EnvAgentUID, v)
	}
	if v, ok := envValue(ic, EnvAgentGID); !ok || v != "7439" {
		t.Fatalf("%s = %q, want 7439", EnvAgentGID, v)
	}
}
