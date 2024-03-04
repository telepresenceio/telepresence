package agentmap

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

const (
	ServicePortAnnotation = agentconfig.DomainPrefix + "inject-service-port"
	ServiceNameAnnotation = agentconfig.DomainPrefix + "inject-service-name"
	ManagerAppName        = "traffic-manager"
)

type GeneratorConfig interface {
	// Generate generates a configuration for the given workload. If replaceContainers is given it will be used to configure
	// container replacement EXCEPT if existingConfig is not nil, in which replaceContainers will be
	// ignored and the value from existingConfig used. 0 can be conventionally passed in as replaceContainers in this case.
	Generate(
		ctx context.Context,
		wl k8sapi.Workload,
		existingConfig agentconfig.SidecarExt,
	) (sc agentconfig.SidecarExt, err error)
}

var GeneratorConfigFunc func(qualifiedAgentImage string) (GeneratorConfig, error) //nolint:gochecknoglobals // extension point

type BasicGeneratorConfig struct {
	ManagerPort         uint16
	AgentPort           uint16
	APIPort             uint16
	TracingPort         uint16
	QualifiedAgentImage string
	ManagerNamespace    string
	LogLevel            string
	InitResources       *core.ResourceRequirements
	Resources           *core.ResourceRequirements
	PullPolicy          string
	PullSecrets         []core.LocalObjectReference
	AppProtocolStrategy k8sapi.AppProtocolStrategy
}

func (cfg *BasicGeneratorConfig) Generate(
	ctx context.Context,
	wl k8sapi.Workload,
	existingConfig agentconfig.SidecarExt,
) (sc agentconfig.SidecarExt, err error) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "agentmap.Generate")
	defer tracing.EndAndRecord(span, err)

	pod := wl.GetPodTemplate()
	pod.Namespace = wl.GetNamespace()
	cns := pod.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agentconfig.ContainerName {
			continue
		}
		ports := cn.Ports
		for pi := range ports {
			if ports[pi].ContainerPort == int32(cfg.AgentPort) {
				return nil, fmt.Errorf(
					"the %s.%s pod container %s is exposing the same port (%d) as the %s sidecar",
					pod.Name, pod.Namespace, cn.Name, cfg.AgentPort, agentconfig.ContainerName)
			}
		}
	}

	svcs, err := findServicesForPod(ctx, pod, pod.Annotations[ServiceNameAnnotation])
	if err != nil {
		return nil, err
	}

	var ccs []*agentconfig.Container
	pns := make(map[int32]uint16)
	portNumber := func(cnPort int32) uint16 {
		if p, ok := pns[cnPort]; ok {
			// Port already mapped. Reuse that mapping
			return p
		}
		p := cfg.AgentPort + uint16(len(pns))
		pns[cnPort] = p
		return p
	}

	for _, svc := range svcs {
		svcImpl, _ := k8sapi.ServiceImpl(svc)
		if ccs, err = appendAgentContainerConfigs(ctx, svcImpl, pod, portNumber, ccs, existingConfig, cfg.AppProtocolStrategy); err != nil {
			return nil, err
		}
	}
	if len(ccs) == 0 {
		return nil, fmt.Errorf("found no service with a port that matches a container in pod %s.%s", pod.Name, pod.Namespace)
	}

	ag := &agentconfig.Sidecar{
		AgentImage:    cfg.QualifiedAgentImage,
		AgentName:     wl.GetName(),
		LogLevel:      cfg.LogLevel,
		Namespace:     wl.GetNamespace(),
		WorkloadName:  wl.GetName(),
		WorkloadKind:  wl.GetKind(),
		ManagerHost:   ManagerAppName + "." + cfg.ManagerNamespace,
		ManagerPort:   cfg.ManagerPort,
		APIPort:       cfg.APIPort,
		TracingPort:   cfg.TracingPort,
		Containers:    ccs,
		InitResources: cfg.InitResources,
		Resources:     cfg.Resources,
		PullPolicy:    cfg.PullPolicy,
		PullSecrets:   cfg.PullSecrets,
	}
	ag.RecordInSpan(span)
	return ag, nil
}

func appendAgentContainerConfigs(
	ctx context.Context,
	svc *core.Service,
	pod *core.PodTemplateSpec,
	portNumber func(int32) uint16,
	ccs []*agentconfig.Container,
	existingConfig agentconfig.SidecarExt,
	aps k8sapi.AppProtocolStrategy,
) ([]*agentconfig.Container, error) {
	portNameOrNumber := pod.Annotations[ServicePortAnnotation]
	ports, err := filterServicePorts(svc, portNameOrNumber)
	if err != nil {
		return nil, err
	}
	ignoredVolumeMounts := agentconfig.GetIgnoredVolumeMounts(pod.ObjectMeta.Annotations)
nextSvcPort:
	for _, port := range ports {
		cn, i := findContainerMatchingPort(&port, pod.Spec.Containers)
		if cn == nil || cn.Name == agentconfig.ContainerName {
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

		ic := &agentconfig.Intercept{
			ServiceName:       svc.Name,
			ServiceUID:        svc.UID,
			ServicePortName:   port.Name,
			ServicePort:       uint16(port.Port),
			TargetPortNumeric: port.TargetPort.Type == intstr.Int,
			Protocol:          port.Protocol,
			AppProtocol:       k8sapi.GetAppProto(ctx, aps, &port),
			AgentPort:         portNumber(appPort.ContainerPort),
			ContainerPortName: appPort.Name,
			ContainerPort:     uint16(appPort.ContainerPort),
		}

		// Validate that we're not being asked to clobber an existing configuration
		var replaceContainer agentconfig.ReplacePolicy
		if existingConfig != nil {
			for _, cc := range existingConfig.AgentConfig().Containers {
				if cc.Name == cn.Name {
					replaceContainer = cc.Replace
					break
				}
			}
		}

		// The container might already have intercepts declared
		for _, cc := range ccs {
			if cc.Name == cn.Name {
				cc.Intercepts = append(cc.Intercepts, ic)
				continue nextSvcPort
			}
		}
		var mounts []string
		if l := len(cn.VolumeMounts); l > 0 {
			mounts = make([]string, 0, l)
			for _, vm := range cn.VolumeMounts {
				if !ignoredVolumeMounts.IsVolumeIgnored(vm.Name, vm.MountPath) {
					mounts = append(mounts, vm.MountPath)
				}
			}
		}
		ccs = append(ccs, &agentconfig.Container{
			Name:       cn.Name,
			EnvPrefix:  CapsBase26(uint64(len(ccs))) + "_",
			MountPoint: agentconfig.MountPrefixApp + "/" + cn.Name,
			Mounts:     mounts,
			Intercepts: []*agentconfig.Intercept{ic},
			Replace:    replaceContainer,
		})
	}
	return ccs, nil
}

// filterServicePorts iterates through a list of ports in a service and
// only returns the ports that match the given nameOrNumber. All ports will
// be returned if nameOrNumber is equal to the empty string.
func filterServicePorts(svc *core.Service, nameOrNumber string) ([]core.ServicePort, error) {
	ports := svc.Spec.Ports
	if nameOrNumber == "" {
		return ports, nil
	}
	svcPorts := make([]core.ServicePort, 0)
	if number, err := strconv.Atoi(nameOrNumber); err != nil {
		errs := validation.IsValidPortName(nameOrNumber)
		if len(errs) > 0 {
			return nil, fmt.Errorf(strings.Join(errs, "\n"))
		}
		for _, port := range ports {
			if port.Name == nameOrNumber {
				svcPorts = append(svcPorts, port)
			}
		}
	} else {
		for _, port := range ports {
			pn := int32(0)
			if port.TargetPort.Type == intstr.Int {
				pn = port.TargetPort.IntVal
			}
			if pn == 0 {
				pn = port.Port
			}
			if pn == int32(number) {
				svcPorts = append(svcPorts, port)
			}
		}
	}
	return svcPorts, nil
}
