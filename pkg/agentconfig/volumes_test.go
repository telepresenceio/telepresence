package agentconfig

import (
	"testing"

	core "k8s.io/api/core/v1"
)

func findVolume(vs []core.Volume, name string) (core.Volume, bool) {
	for _, v := range vs {
		if v.Name == name {
			return v, true
		}
	}
	return core.Volume{}, false
}

func TestAgentVolumesAddsCoverHostPath(t *testing.T) {
	vs, err := AgentVolumes("test-agent", nil, "/rtest-coverage")
	if err != nil {
		t.Fatal(err)
	}
	v, ok := findVolume(vs, CoverVolumeName)
	if !ok {
		t.Fatalf("%s volume not found", CoverVolumeName)
	}
	hp := v.HostPath
	if hp == nil || hp.Path != "/rtest-coverage" {
		t.Fatalf("HostPath = %+v, want Path /rtest-coverage", hp)
	}
	if hp.Type == nil || *hp.Type != core.HostPathDirectoryOrCreate {
		t.Fatalf("HostPath.Type = %v, want %v", hp.Type, core.HostPathDirectoryOrCreate)
	}
}

func TestAgentVolumesOmitsCoverHostPathWhenUnset(t *testing.T) {
	vs, err := AgentVolumes("test-agent", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findVolume(vs, CoverVolumeName); ok {
		t.Fatalf("%s volume present, want absent", CoverVolumeName)
	}
}
