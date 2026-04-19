package agentconfig

import (
	"testing"

	core "k8s.io/api/core/v1"
)

func TestInitContainerSetsAgentUID(t *testing.T) {
	uid := int64(1000)
	ic := InitContainer(&Sidecar{AgentImage: "traffic-agent"}, &core.SecurityContext{RunAsUser: &uid})

	for _, env := range ic.Env {
		if env.Name == EnvAgentUID {
			if env.Value != "1000" {
				t.Fatalf("%s = %q, want 1000", EnvAgentUID, env.Value)
			}
			return
		}
	}
	t.Fatalf("%s was not set", EnvAgentUID)
}

func TestInitContainerOmitsAgentUIDWhenUnknown(t *testing.T) {
	ic := InitContainer(&Sidecar{AgentImage: "traffic-agent"}, nil)

	for _, env := range ic.Env {
		if env.Name == EnvAgentUID {
			t.Fatalf("%s = %q, want unset", EnvAgentUID, env.Value)
		}
	}
}
