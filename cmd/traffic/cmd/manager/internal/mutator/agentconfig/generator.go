package agentconfig

import (
	"context"
	"fmt"
	"strings"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/install/agent"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	ServicePortAnnotation = agent.DomainPrefix + "inject-service-port"
	ServiceNameAnnotation = agent.DomainPrefix + "inject-service-name"
	ManagerAppName        = "traffic-manager"
	ManagerPortHTTP       = 8081
)

func Generate(ctx context.Context, wl k8sapi.Workload, pod *core.PodTemplateSpec) (*agent.Config, error) {
	env := managerutil.GetEnv(ctx)
	pod := wl.GetPodTemplate()
	pod.Namespace = wl.GetNamespace()
	cns := pod.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agent.ContainerName {
			continue
		}
		ports := cn.Ports
		for pi := range ports {
			if ports[pi].ContainerPort == env.AgentPort {
				return nil, fmt.Errorf(
					"the %s.%s pod container %s is exposing the same port (%d) as the %s sidecar",
					pod.Name, pod.Namespace, cn.Name, env.AgentPort, agent.ContainerName)
			}
		}
	}

	svcs, err := findServicesForPod(ctx, pod, pod.Annotations[ServiceNameAnnotation])
	if err != nil {
		return nil, err
	}

	var ccs []*agent.Container
	agentPort := uint16(env.AgentPort)
	for _, svc := range svcs {
		svcImpl, _ := k8sapi.ServiceImpl(svc)
		if ccs, err = appendAgentContainerConfigs(svcImpl, pod, &agentPort, ccs); err != nil {
			return nil, err
		}
	}
	if len(ccs) == 0 {
		return nil, fmt.Errorf("found no service with a port that matches a container in pod %s.%s", pod.Name, pod.Namespace)
	}

	ag := &agent.Config{
		AgentImage:   env.AgentRegistry + "/" + env.AgentImage,
		AgentName:    AgentName(wl),
		Namespace:    wl.GetNamespace(),
		WorkloadName: wl.GetName(),
		WorkloadKind: wl.GetKind(),
		ManagerHost:  ManagerAppName + "." + env.ManagerNamespace,
		ManagerPort:  ManagerPortHTTP,
		APIPort:      uint16(env.APIPort),
		Containers:   ccs,
	}
	return ag, nil
}

func appendAgentContainerConfigs(svc *core.Service, pod *core.PodTemplateSpec, portNumber *uint16, ccs []*agent.Container) ([]*agent.Container, error) {
	portNameOrNumber := pod.Annotations[ServicePortAnnotation]
	ports, err := install.FilterServicePorts(svc, portNameOrNumber)
	if err != nil {
		return nil, err
	}
nextSvcPort:
	for _, port := range ports {
		cn, i := findContainerMatchingPort(&port, pod.Spec.Containers)
		if cn == nil || cn.Name == agent.ContainerName {
			continue
		}
		var appPort core.ContainerPort
		if i < 0 {
			// Can only happen if the service port is numeric, so it's safe to use TargetPort.IntVal here
			appPort = core.ContainerPort{
				Protocol:      port.Protocol,
				ContainerPort: port.TargetPort.IntVal,
			}
		} else {
			appPort = cn.Ports[i]
		}
		var appProto string
		if port.AppProtocol != nil {
			appProto = *port.AppProtocol
		}

		ic := &agent.Intercept{
			ServiceName:       svc.Name,
			ServiceUID:        svc.UID,
			ServicePortName:   port.Name,
			ServicePort:       uint16(port.Port),
			Protocol:          string(port.Protocol),
			AppProtocol:       appProto,
			AgentPort:         *portNumber,
			ContainerPortName: appPort.Name,
			ContainerPort:     uint16(appPort.ContainerPort),
		}
		*portNumber++

		// The container might already have intercepts declared
		for _, cc := range ccs {
			if cc.Name == cn.Name {
				cc.Intercepts = append(cc.Intercepts, ic)
				continue nextSvcPort
			}
		}
		var mounts []string
		if l := len(cn.VolumeMounts); l > 0 {
			mounts = make([]string, l)
			for i, vm := range cn.VolumeMounts {
				mounts[i] = vm.MountPath
			}
		}
		ccs = append(ccs, &agent.Container{
			Name:       cn.Name,
			EnvPrefix:  CapsBase26(uint64(len(ccs))) + "_",
			MountPoint: agent.MountPrefixApp + "/" + cn.Name,
			Mounts:     mounts,
			Intercepts: []*agent.Intercept{ic},
		})
	}
	return ccs, nil
}

func AgentName(wl k8sapi.Workload) string {
	switch wl.GetKind() {
	case "ReplicaSet":
		// If it's owned by a replicaset, then it's the same as the deployment e.g. "my-echo-697464c6c5" -> "my-echo"
		tokens := strings.Split(wl.GetName(), "-")
		return strings.Join(tokens[:len(tokens)-1], "-")
	default:
		// If the pod is owned by a statefulset, or a deployment, the agent's name is the same as the workload's
		return wl.GetName()
	}
}
