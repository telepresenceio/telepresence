package agentconfig

import (
	"testing"

	core "k8s.io/api/core/v1"
)

func scBuilder(podSc *core.PodSecurityContext, agentSc *core.SecurityContext, appSc *core.SecurityContext, extraCns ...core.Container) *ContainerBuilder {
	return &ContainerBuilder{
		Pod: &core.PodTemplateSpec{
			Spec: core.PodSpec{
				SecurityContext: podSc,
				Containers: append([]core.Container{
					{
						Name:            "app",
						SecurityContext: appSc,
					},
				}, extraCns...),
			},
		},
		Config: &Sidecar{
			SecurityContext: agentSc,
			Containers: []*Container{
				{
					Name:       "app",
					Intercepts: []*Intercept{{ContainerPort: 8080}},
				},
			},
		},
	}
}

func TestAgentSecurityContextInheritsAppWithDistinctGroup(t *testing.T) {
	appSc := &core.SecurityContext{RunAsUser: new(int64(1000)), RunAsGroup: new(int64(1000))}
	sc, err := scBuilder(nil, nil, appSc).AgentSecurityContext()
	if err != nil {
		t.Fatal(err)
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 1000 {
		t.Fatalf("RunAsUser = %v, want 1000", sc.RunAsUser)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != DefaultAgentGID {
		t.Fatalf("RunAsGroup = %v, want %d", sc.RunAsGroup, DefaultAgentGID)
	}
	if *appSc.RunAsGroup != 1000 {
		t.Fatalf("app securityContext was mutated: RunAsGroup = %d", *appSc.RunAsGroup)
	}
}

func TestAgentSecurityContextSetsGroupWithoutAppContext(t *testing.T) {
	sc, err := scBuilder(nil, nil, nil).AgentSecurityContext()
	if err != nil {
		t.Fatal(err)
	}
	if sc == nil || sc.RunAsGroup == nil || *sc.RunAsGroup != DefaultAgentGID {
		t.Fatalf("RunAsGroup = %v, want %d", sc.RunAsGroup, DefaultAgentGID)
	}
	if sc.RunAsUser != nil {
		t.Fatalf("RunAsUser = %d, want unset", *sc.RunAsUser)
	}
}

func TestAgentSecurityContextRespectsConfiguredGroup(t *testing.T) {
	agentSc := &core.SecurityContext{RunAsGroup: new(int64(3000))}
	sc, err := scBuilder(nil, agentSc, nil).AgentSecurityContext()
	if err != nil {
		t.Fatal(err)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != 3000 {
		t.Fatalf("RunAsGroup = %v, want 3000", sc.RunAsGroup)
	}
}

func TestAgentSecurityContextAddsGroupToConfigured(t *testing.T) {
	agentSc := &core.SecurityContext{RunAsUser: new(int64(2000))}
	sc, err := scBuilder(nil, agentSc, nil).AgentSecurityContext()
	if err != nil {
		t.Fatal(err)
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 2000 {
		t.Fatalf("RunAsUser = %v, want 2000", sc.RunAsUser)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != DefaultAgentGID {
		t.Fatalf("RunAsGroup = %v, want %d", sc.RunAsGroup, DefaultAgentGID)
	}
	if agentSc.RunAsGroup != nil {
		t.Fatalf("configured securityContext was mutated: RunAsGroup = %d", *agentSc.RunAsGroup)
	}
}

func TestAgentSecurityContextBumpsCollidingGroup(t *testing.T) {
	appSc := &core.SecurityContext{RunAsGroup: new(DefaultAgentGID)}
	other := core.Container{
		Name:            "other",
		SecurityContext: &core.SecurityContext{RunAsGroup: new(DefaultAgentGID + 1)},
	}
	sc, err := scBuilder(nil, nil, appSc, other).AgentSecurityContext()
	if err != nil {
		t.Fatal(err)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != DefaultAgentGID+2 {
		t.Fatalf("RunAsGroup = %v, want %d", sc.RunAsGroup, DefaultAgentGID+2)
	}
}

func TestAgentSecurityContextBumpsGroupCollidingWithPod(t *testing.T) {
	podSc := &core.PodSecurityContext{RunAsGroup: new(DefaultAgentGID)}
	sc, err := scBuilder(podSc, nil, nil).AgentSecurityContext()
	if err != nil {
		t.Fatal(err)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != DefaultAgentGID+1 {
		t.Fatalf("RunAsGroup = %v, want %d", sc.RunAsGroup, DefaultAgentGID+1)
	}
}

func TestAgentSecurityContextIgnoresOwnContainersOnReinjection(t *testing.T) {
	agentCn := core.Container{
		Name:            ContainerName,
		SecurityContext: &core.SecurityContext{RunAsGroup: new(DefaultAgentGID)},
	}
	sc, err := scBuilder(nil, nil, nil, agentCn).AgentSecurityContext()
	if err != nil {
		t.Fatal(err)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != DefaultAgentGID {
		t.Fatalf("RunAsGroup = %v, want %d", sc.RunAsGroup, DefaultAgentGID)
	}
}

func Test_prefixInterpolated(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{
			"empty",
			"",
			"",
		},
		{
			"empty_ipl",
			"$()",
			"$()",
		},
		{
			"alone",
			"$(IPL)",
			"$(_TEL_APP_A_IPL)",
		},
		{
			"normal",
			"Normal $(IPL) text",
			"Normal $(_TEL_APP_A_IPL) text",
		},
		{
			"escaped_ipl",
			"Escaped $$(IPL) text",
			"Escaped $$(IPL) text",
		},
		{
			"nested_ipl",
			"Nested $(IP$(IPL)) text",
			"Nested $(IP$(_TEL_APP_A_IPL)) text",
		},
		{
			"invalid_env",
			"Nested $(IP$) text",
			"Nested $(IP$) text",
		},
		{
			"unbalanced",
			"Unbalanced $(IPL text",
			"Unbalanced $(IPL text",
		},
		{
			"adjacent",
			"Adjacent $(IP1)$(IP2) text",
			"Adjacent $(_TEL_APP_A_IP1)$(_TEL_APP_A_IP2) text",
		},
		{
			"dollar-separated",
			"Dollar $(IP1)$$$(IP2) separated",
			"Dollar $(_TEL_APP_A_IP1)$$$(_TEL_APP_A_IP2) separated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prefixInterpolated(tt.arg, "_TEL_APP_A_"); got != tt.want {
				t.Errorf("prefixInterpolated(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}
