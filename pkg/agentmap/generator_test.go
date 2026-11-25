package agentmap

import (
	"strings"
	"testing"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestApplyInactivePortAnnotation(t *testing.T) {
	workload := deploymentWithInactivePort("8399")
	intercepts := []*agentconfig.Intercept{
		{ServiceName: "app", ContainerPort: 8000, AgentPort: 9900, Protocol: types.ProtoTCP},
		{ServiceName: "app-websocket", ContainerPort: 8000, AgentPort: 9900, Protocol: types.ProtoTCP},
	}
	containers := []*agentconfig.Container{{Name: "app", Intercepts: intercepts}}

	if err := applyInactivePortAnnotation(workload, containers); err != nil {
		t.Fatal(err)
	}
	for _, intercept := range intercepts {
		if intercept.InactivePort != 8399 {
			t.Fatalf("InactivePort = %d, want 8399", intercept.InactivePort)
		}
	}
}

func TestApplyInactivePortAnnotationRejectsMultipleTargets(t *testing.T) {
	workload := deploymentWithInactivePort("8399")
	containers := []*agentconfig.Container{{
		Name: "app",
		Intercepts: []*agentconfig.Intercept{
			{ContainerPort: 8000, AgentPort: 9900, Protocol: types.ProtoTCP},
			{ContainerPort: 9000, AgentPort: 9901, Protocol: types.ProtoTCP},
		},
	}}

	err := applyInactivePortAnnotation(workload, containers)
	if err == nil || !strings.Contains(err.Error(), "requires exactly one intercepted container port") {
		t.Fatalf("applyInactivePortAnnotation() error = %v", err)
	}
}

func deploymentWithInactivePort(port string) k8sapi.Workload {
	return k8sapi.Deployment(&apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: apps.DeploymentSpec{Template: core.PodTemplateSpec{ObjectMeta: meta.ObjectMeta{
			Annotations: map[string]string{annotation.InjectInactivePort: port},
		}}},
	})
}

func TestManagerHostUsesClusterDomain(t *testing.T) {
	got := ManagerHost("traffic", "cluster.local.")
	want := "traffic-manager.traffic.svc.cluster.local"
	if got != want {
		t.Fatalf("ManagerHost() = %q, want %q", got, want)
	}
}

func TestManagerHostKeepsLegacyHostWhenClusterDomainUnknown(t *testing.T) {
	got := ManagerHost("traffic", "")
	want := "traffic-manager.traffic"
	if got != want {
		t.Fatalf("ManagerHost() = %q, want %q", got, want)
	}
}

func serviceLessPod(containers ...core.Container) *core.PodTemplateSpec {
	return &core.PodTemplateSpec{Spec: core.PodSpec{Containers: containers}}
}

func TestUndeclaredNumericPortOnMultiContainerPodIsUserError(t *testing.T) {
	pod := serviceLessPod(
		core.Container{Name: "a", Ports: []core.ContainerPort{{ContainerPort: 8080, Protocol: core.ProtocolTCP}}},
		core.Container{Name: "b"},
	)
	cfg := &GeneratorConfig{AgentPort: 9900}
	_, err := cfg.appendServiceLessAgentContainerConfigs(pod, []types.PortIdentifier{"9999"},
		func(int32) uint16 { return 9900 }, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "9999") {
		t.Fatalf("error = %v, want one naming port 9999", err)
	}
	if errcat.GetCategory(err) != errcat.User {
		t.Fatalf("error category = %v, want User", errcat.GetCategory(err))
	}
}

func TestUndeclaredNumericPortOnSingleContainerPodIsAssigned(t *testing.T) {
	pod := serviceLessPod(core.Container{Name: "app"})
	cfg := &GeneratorConfig{AgentPort: 9900}
	ccs, err := cfg.appendServiceLessAgentContainerConfigs(pod, []types.PortIdentifier{"9999"},
		func(int32) uint16 { return 9900 }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ccs) != 1 || ccs[0].Name != "app" || len(ccs[0].Intercepts) != 1 || ccs[0].Intercepts[0].ContainerPort != 9999 {
		t.Fatalf("unexpected container configs: %+v", ccs)
	}
}
