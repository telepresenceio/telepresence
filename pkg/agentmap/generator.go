package agentmap

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

var TrafficManagerSelector = labels.SelectorFromSet(map[string]string{ //nolint:gochecknoglobals // constant
	"app":          agentconfig.ManagerAppName,
	"telepresence": "manager",
})

type GeneratorConfig interface {
	// Generate generates a configuration for the given workload.
	Generate(ctx context.Context, wl k8sapi.Workload, existingConfig agentconfig.SidecarExt) (sc agentconfig.SidecarExt, err error)
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
	InitSecurityContext *core.SecurityContext
	MountPolicies       types.MountPolicies
}

func portsFromContainerPortsAnnotation(ctx context.Context, wl k8sapi.Workload) (ports []types.PortIdentifier, err error) {
	pod := wl.GetPodTemplate()
	cpa := annotation.GetAnnotation(ctx, pod.GetAnnotations(), annotation.InjectContainerPorts, annotation.LegacyInjectContainerPorts)
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
				ports = append(ports, types.PortIdentifier(pi))
			}
		}
	default:
		ports, err = portsFromAnnotationValue(wl, annotation.InjectContainerPorts, cpa)
	}
	return ports, err
}

func portsFromAnnotationValue(wl k8sapi.Workload, annotation, value string) (ports []types.PortIdentifier, err error) {
	cps := strings.Split(value, ",")
	ports = make([]types.PortIdentifier, len(cps))
	for i, cp := range cps {
		pi := types.PortIdentifier(cp)
		if err = pi.Validate(); err != nil {
			return nil, fmt.Errorf("unable to parse annotation %s of %s: %w", annotation, wl, err)
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
		return nil, fmt.Errorf("%s is the Telepresence Traffic Manager. It can not have a traffic-agent", wl)
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

	ann := annotation.GetAnnotation(ctx, pod.Annotations, annotation.InjectServiceName, annotation.LegacyInjectServiceName)
	svcs, err := FindServicesForPod(ctx, pod, ann)
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

	var ports []types.PortIdentifier
	ann = annotation.GetAnnotation(ctx, pod.Annotations, annotation.InjectServicePorts, annotation.LegacyInjectServicePort)
	if ann != "" {
		ports, err = portsFromAnnotationValue(wl, annotation.InjectServicePorts, ann)
	}
	if err != nil {
		return nil, err
	}
	cfg.MountPolicies, err = cfg.MountPolicies.AddAnnotations(ctx, pod.Annotations)
	if err != nil {
		return nil, err
	}
	var ccs []*agentconfig.Container
	for _, svc := range svcs {
		svcImpl, _ := k8sapi.ServiceImpl(svc)
		ccs = cfg.appendAgentContainerConfigs(ctx, svcImpl, pod, ports, agentPortNumberFunc, ccs, existingConfig)
	}

	ports, err = portsFromContainerPortsAnnotation(ctx, wl)
	if err != nil {
		return nil, err
	}
	if len(ports) > 0 {
		if ccs, err = cfg.appendServiceLessAgentContainerConfigs(ctx, pod, ports, agentPortNumberFunc, ccs, existingConfig); err != nil {
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
			ccs = append(ccs, cfg.newContainerConfig(cn, len(ccs), nil, containerReplacePolicy(existingConfig, cn)))
		}
	}

	return &agentconfig.Sidecar{
		AgentImage:          cfg.QualifiedAgentImage,
		AgentName:           wl.GetName(),
		LogLevel:            cfg.LogLevel,
		Namespace:           wl.GetNamespace(),
		WorkloadName:        wl.GetName(),
		WorkloadKind:        wl.GetKind(),
		ManagerHost:         agentconfig.ManagerAppName + "." + cfg.ManagerNamespace,
		ManagerPort:         cfg.ManagerPort,
		APIPort:             cfg.APIPort,
		MountPolicies:       cfg.MountPolicies,
		Containers:          ccs,
		InitResources:       cfg.InitResources,
		Resources:           cfg.Resources,
		PullPolicy:          cfg.PullPolicy,
		PullSecrets:         cfg.PullSecrets,
		SecurityContext:     cfg.SecurityContext,
		InitSecurityContext: cfg.InitSecurityContext,
	}, nil
}

func (cfg *BasicGeneratorConfig) appendAgentContainerConfigs(
	ctx context.Context,
	svc *core.Service,
	pod *core.PodTemplateSpec,
	portAnnotations []types.PortIdentifier,
	agentPortNumberFunc func(int32) uint16,
	ccs []*agentconfig.Container,
	existingConfig agentconfig.SidecarExt,
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
			AppProtocol:       k8sapi.GetAppProto(ctx, cfg.AppProtocolStrategy, &port),
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
		ccs = append(ccs, cfg.newContainerConfig(cn, len(ccs), []*agentconfig.Intercept{ic}, containerReplacePolicy(existingConfig, cn)))
	}
	return ccs
}

func (cfg *BasicGeneratorConfig) newContainerConfig(cn *core.Container, index int, ics []*agentconfig.Intercept, rp agentconfig.ReplacePolicy) *agentconfig.Container {
	// Create the concrete MountPolicies that map mount path to policy
	var mps types.MountPolicies
	for i := range cn.VolumeMounts {
		vm := &cn.VolumeMounts[i]
		path := vm.MountPath
		vp := cfg.MountPolicies.Get(vm.Name, path)
		if vp != types.MountPolicyIgnore {
			if mps == nil {
				mps = make(types.MountPolicies, len(cn.VolumeMounts))
			}
			mps[path] = vp
		}
	}
	// Legacy mounts property must list all remote mounts, no more and no less
	var mounts []string
	if len(mps) > 0 {
		mounts = make([]string, 0, len(mps))
		for key, mp := range mps {
			if mp == types.MountPolicyRemote || mp == types.MountPolicyRemoteReadOnly {
				mounts = append(mounts, key)
			}
		}
	}
	sort.Strings(mounts)
	return &agentconfig.Container{
		Name:       cn.Name,
		EnvPrefix:  CapsBase26(uint64(index)) + "_",
		MountPoint: agentconfig.MountPrefixApp + "/" + cn.Name,
		MountPaths: mounts,
		Mounts:     mps,
		Intercepts: ics,
		Replace:    rp,
	}
}

func findContainerPort(cns []core.Container, p types.PortIdentifier) (*core.Container, *core.ContainerPort) {
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

func (cfg *BasicGeneratorConfig) appendServiceLessAgentContainerConfigs(
	ctx context.Context,
	pod *core.PodTemplateSpec,
	portAnnotations []types.PortIdentifier,
	agentPortNumberFunc func(int32) uint16,
	ccs []*agentconfig.Container,
	existingConfig agentconfig.SidecarExt,
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
			AppProtocol:       getContainerPortAppProtocol(ctx, cfg.AppProtocolStrategy, appPort.Name),
			ContainerPortName: appPort.Name,
			ContainerPort:     uint16(appPort.ContainerPort),
		}

		// The container might already have intercepts declared
		for _, cc := range ccs {
			if cc.Name == cn.Name {
				// Don't add service-less intercept if an intercept with a service is present
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
		ccs = append(ccs, cfg.newContainerConfig(cn, len(ccs), []*agentconfig.Intercept{ic}, containerReplacePolicy(existingConfig, cn)))
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
func filterServicePorts(svc *core.Service, portAnnotations []types.PortIdentifier) []core.ServicePort {
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
