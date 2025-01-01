package agentmap

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	ContainerPortsAnnotation = agentconfig.DomainPrefix + "inject-container-ports"
	ServicePortsAnnotation   = agentconfig.DomainPrefix + "inject-service-ports"
	// ServicePortAnnotation is deprecated. Use plural form instead.
	ServicePortAnnotation = agentconfig.DomainPrefix + "inject-service-port"
	ServiceNameAnnotation = agentconfig.DomainPrefix + "inject-service-name"
	ManagerAppName        = "traffic-manager"
)

var TrafficManagerSelector = labels.SelectorFromSet(map[string]string{ //nolint:gochecknoglobals // constant
	"app":          ManagerAppName,
	"telepresence": "manager",
})

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
	QualifiedAgentImage string
	ManagerNamespace    string
	LogLevel            string
	InitResources       *core.ResourceRequirements
	Resources           *core.ResourceRequirements
	PullPolicy          string
	PullSecrets         []core.LocalObjectReference
	AppProtocolStrategy k8sapi.AppProtocolStrategy
	SecurityContext     *core.SecurityContext
}

func portsFromContainerPortsAnnotation(wl k8sapi.Workload) (ports []agentconfig.PortIdentifier, err error) {
	pod := wl.GetPodTemplate()
	cpa := pod.GetAnnotations()[ContainerPortsAnnotation]
	switch cpa {
	case "":
		return nil, nil
	case "all":
		cns := pod.Spec.Containers
		for i := range cns {
			for _, pn := range cns[i].Ports {
				pi := pn.Name
				if pi == "" {
					pi = strconv.Itoa(int(pn.ContainerPort))
				}
				if pn.Protocol != core.ProtocolTCP {
					pi += "/" + string(pn.Protocol)
				}
				ports = append(ports, agentconfig.PortIdentifier(pi))
			}
		}
	default:
		ports, err = portsFromAnnotationValue(wl, ContainerPortsAnnotation, cpa)
	}
	return ports, err
}

func portsFromAnnotation(wl k8sapi.Workload, annotation string) (ports []agentconfig.PortIdentifier, err error) {
	if cpa := wl.GetPodTemplate().GetAnnotations()[annotation]; cpa != "" {
		ports, err = portsFromAnnotationValue(wl, annotation, cpa)
	}
	return ports, err
}

func portsFromAnnotationValue(wl k8sapi.Workload, annotation, value string) (ports []agentconfig.PortIdentifier, err error) {
	cps := strings.Split(value, ",")
	ports = make([]agentconfig.PortIdentifier, len(cps))
	for i, cp := range cps {
		pi := agentconfig.PortIdentifier(cp)
		if err = pi.Validate(); err != nil {
			return nil, fmt.Errorf("unable to parse annotation %s of workload %s.%s: %w", annotation, wl.GetName(), wl.GetNamespace(), err)
		}
		ports[i] = pi
	}
	return ports, nil
}

func (cfg *BasicGeneratorConfig) Generate(
	ctx context.Context,
	wl k8sapi.Workload,
	existingConfig agentconfig.SidecarExt,
) (sc agentconfig.SidecarExt, err error) {
	if TrafficManagerSelector.Matches(labels.Set(wl.GetLabels())) {
		return nil, fmt.Errorf("deployment %s.%s is the Telepresence Traffic Manager. It can not have a traffic-agent", wl.GetName(), wl.GetNamespace())
	}

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

	svcs, err := FindServicesForPod(ctx, pod, pod.Annotations[ServiceNameAnnotation])
	if err != nil {
		return nil, err
	}

	pns := make(map[int32]uint16)
	agentPortNumberFunc := func(cnPort int32) uint16 {
		if p, ok := pns[cnPort]; ok {
			// Port already mapped. Reuse that mapping
			return p
		}
		p := cfg.AgentPort + uint16(len(pns))
		pns[cnPort] = p
		return p
	}

	ports, err := portsFromAnnotation(wl, ServicePortsAnnotation)
	if err == nil && len(ports) == 0 {
		// Check singular form.
		ports, err = portsFromAnnotation(wl, ServicePortAnnotation)
		if len(ports) > 0 {
			dlog.Warningf(ctx, "the %q annotation is deprecated. Use plural form %q instead", ServicePortAnnotation, ServicePortsAnnotation)
		}
	}
	if err != nil {
		return nil, err
	}
	ignoredVolumeMounts := agentconfig.GetIgnoredVolumeMounts(pod.Annotations)
	var ccs []*agentconfig.Container
	for _, svc := range svcs {
		svcImpl, _ := k8sapi.ServiceImpl(svc)
		ccs = appendAgentContainerConfigs(ctx, svcImpl, pod, ports, agentPortNumberFunc, ccs, existingConfig, cfg.AppProtocolStrategy, ignoredVolumeMounts)
	}

	ports, err = portsFromContainerPortsAnnotation(wl)
	if err != nil {
		return nil, err
	}
	if len(ports) > 0 {
		if ccs, err = appendServiceLessAgentContainerConfigs(ctx, pod, ports, agentPortNumberFunc, ccs, existingConfig, cfg.AppProtocolStrategy, ignoredVolumeMounts); err != nil {
			return nil, err
		}
	}

	// Append other containers even though they aren't directly interceptable. They might be fronted by a
	// dispatching container that is, or they might be candidates for `ingest`.
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agentconfig.ContainerName {
			continue
		}
		if !slices.ContainsFunc(ccs, func(cc *agentconfig.Container) bool { return cc.Name == cn.Name }) {
			ccs = append(ccs, &agentconfig.Container{
				Name:       cn.Name,
				EnvPrefix:  CapsBase26(uint64(len(ccs))) + "_",
				MountPoint: agentconfig.MountPrefixApp + "/" + cn.Name,
				Mounts:     containerMounts(cn, ignoredVolumeMounts),
				Replace:    containerReplacePolicy(existingConfig, cn),
			})
		}
	}

	return &agentconfig.Sidecar{
		AgentImage:      cfg.QualifiedAgentImage,
		AgentName:       wl.GetName(),
		LogLevel:        cfg.LogLevel,
		Namespace:       wl.GetNamespace(),
		WorkloadName:    wl.GetName(),
		WorkloadKind:    wl.GetKind(),
		ManagerHost:     ManagerAppName + "." + cfg.ManagerNamespace,
		ManagerPort:     cfg.ManagerPort,
		APIPort:         cfg.APIPort,
		Containers:      ccs,
		InitResources:   cfg.InitResources,
		Resources:       cfg.Resources,
		PullPolicy:      cfg.PullPolicy,
		PullSecrets:     cfg.PullSecrets,
		SecurityContext: cfg.SecurityContext,
	}, nil
}

func appendAgentContainerConfigs(
	ctx context.Context,
	svc *core.Service,
	pod *core.PodTemplateSpec,
	portAnnotations []agentconfig.PortIdentifier,
	agentPortNumberFunc func(int32) uint16,
	ccs []*agentconfig.Container,
	existingConfig agentconfig.SidecarExt,
	aps k8sapi.AppProtocolStrategy,
	ignoredVolumeMounts agentconfig.IgnoredVolumeMounts,
) []*agentconfig.Container {
	ports := filterServicePorts(svc, portAnnotations)
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
			AgentPort:         agentPortNumberFunc(appPort.ContainerPort),
			ContainerPortName: appPort.Name,
			ContainerPort:     uint16(appPort.ContainerPort),
		}

		// The container might already have intercepts declared
		for _, cc := range ccs {
			if cc.Name == cn.Name {
				cc.Intercepts = append(cc.Intercepts, ic)
				continue nextSvcPort
			}
		}
		ccs = append(ccs, &agentconfig.Container{
			Name:       cn.Name,
			EnvPrefix:  CapsBase26(uint64(len(ccs))) + "_",
			MountPoint: agentconfig.MountPrefixApp + "/" + cn.Name,
			Mounts:     containerMounts(cn, ignoredVolumeMounts),
			Intercepts: []*agentconfig.Intercept{ic},
			Replace:    containerReplacePolicy(existingConfig, cn),
		})
	}
	return ccs
}

func findContainerPort(cns []core.Container, p agentconfig.PortIdentifier) (*core.Container, *core.ContainerPort) {
	proto, name, num := p.ProtoAndNameOrNumber()
	for n := range cns {
		cn := &cns[n]
		if cn.Name != agentconfig.ContainerName {
			for i := range cn.Ports {
				appPort := &cn.Ports[i]
				if (name != "" && name == appPort.Name || num == uint16(appPort.ContainerPort)) &&
					(proto == appPort.Protocol || proto == core.ProtocolTCP && appPort.Protocol == "") {
					return cn, appPort
				}
			}
		}
	}
	return nil, nil
}

func appendServiceLessAgentContainerConfigs(
	ctx context.Context,
	pod *core.PodTemplateSpec,
	portAnnotations []agentconfig.PortIdentifier,
	agentPortNumberFunc func(int32) uint16,
	ccs []*agentconfig.Container,
	existingConfig agentconfig.SidecarExt,
	aps k8sapi.AppProtocolStrategy,
	ignoredVolumeMounts agentconfig.IgnoredVolumeMounts,
) ([]*agentconfig.Container, error) {
	cns := pod.Spec.Containers
	anonNameIndex := uint64(0)
nextContainerPort:
	for _, p := range portAnnotations {
		cn, appPort := findContainerPort(cns, p)
		if appPort == nil {
			// The port is not explicitly declared as a container port, so if possible, we synthesize one.
			proto, name, num := p.ProtoAndNameOrNumber()
			if name != "" {
				// We can only synthesize given a numeric port.
				return nil, fmt.Errorf("found no container port that matches port annotation %s", p)
			}
			appPort = &core.ContainerPort{
				Name:          fmt.Sprintf("port-%s", Base26(anonNameIndex)),
				ContainerPort: int32(num),
				Protocol:      proto,
			}
			anonNameIndex++
		}
		ic := &agentconfig.Intercept{
			TargetPortNumeric: true,
			Protocol:          appPort.Protocol,
			AgentPort:         agentPortNumberFunc(appPort.ContainerPort),
			AppProtocol:       getContainerPortAppProtocol(ctx, aps, appPort.Name),
			ContainerPortName: appPort.Name,
			ContainerPort:     uint16(appPort.ContainerPort),
		}

		// The container might already have intercepts declared
		for _, cc := range ccs {
			if cc.Name == cn.Name {
				// Don't add service less intercept if an intercept with a service is present
				cnFound := false
				for _, eic := range cc.Intercepts {
					if eic.ContainerPort == ic.ContainerPort {
						cnFound = true
						break
					}
				}
				if !cnFound {
					cc.Intercepts = append(cc.Intercepts, ic)
				}
				continue nextContainerPort
			}
		}
		ccs = append(ccs, &agentconfig.Container{
			Name:       cn.Name,
			EnvPrefix:  CapsBase26(uint64(len(ccs))) + "_",
			MountPoint: agentconfig.MountPrefixApp + "/" + cn.Name,
			Mounts:     containerMounts(cn, ignoredVolumeMounts),
			Intercepts: []*agentconfig.Intercept{ic},
			Replace:    containerReplacePolicy(existingConfig, cn),
		})
	}
	return ccs, nil
}

func containerReplacePolicy(existingConfig agentconfig.SidecarExt, cn *core.Container) agentconfig.ReplacePolicy {
	var replaceContainer agentconfig.ReplacePolicy
	if existingConfig != nil {
		for _, cc := range existingConfig.AgentConfig().Containers {
			if cc.Name == cn.Name {
				replaceContainer = cc.Replace
				break
			}
		}
	}
	return replaceContainer
}

func containerMounts(cn *core.Container, ignoredVolumeMounts agentconfig.IgnoredVolumeMounts) []string {
	var mounts []string
	if l := len(cn.VolumeMounts); l > 0 {
		mounts = make([]string, 0, l)
		for _, vm := range cn.VolumeMounts {
			if !ignoredVolumeMounts.IsVolumeIgnored(vm.Name, vm.MountPath) {
				mounts = append(mounts, vm.MountPath)
			}
		}
	}
	return mounts
}

func getContainerPortAppProtocol(ctx context.Context, aps k8sapi.AppProtocolStrategy, portName string) string {
	switch aps {
	case k8sapi.Http:
		return "http"
	case k8sapi.Http2:
		return "http2"
	case k8sapi.PortName:
		if portName == "" {
			dlog.Debug(ctx, "Unable to derive application protocol from unnamed container port")
			break
		}
		pn := portName
		if dashPos := strings.IndexByte(pn, '-'); dashPos > 0 {
			pn = pn[:dashPos]
		}
		var appProto string
		switch strings.ToLower(pn) {
		case "http", "https", "grpc", "http2":
			appProto = pn
		case "h2c": // h2c is cleartext HTTP/2
			appProto = "http2"
		case "tls", "h2": // same as https in this context and h2 is HTTP/2 with TLS
			appProto = "https"
		}
		if appProto != "" {
			dlog.Debugf(ctx, "Using application protocol %q derived from port name %q", appProto, portName)
			return appProto
		}
		dlog.Debugf(ctx, "Unable to derive application protocol from port name %q", portName)
	}
	return ""
}

// filterServicePorts iterates through a list of ports in a service and
// only returns the ports that match the given nameOrNumber. All ports will
// be returned if nameOrNumber is equal to the empty string.
func filterServicePorts(svc *core.Service, portAnnotations []agentconfig.PortIdentifier) []core.ServicePort {
	ports := svc.Spec.Ports
	if len(portAnnotations) == 0 {
		return ports
	}
	svcPorts := make([]core.ServicePort, 0)
	for _, pi := range portAnnotations {
		proto, name, num := pi.ProtoAndNameOrNumber()
		if name != "" {
			for _, port := range ports {
				if port.Name == name {
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
				if uint16(pn) == num && (port.Protocol == "" && proto == core.ProtocolTCP || port.Protocol == proto) {
					svcPorts = append(svcPorts, port)
				}
			}
		}
	}
	return svcPorts
}
