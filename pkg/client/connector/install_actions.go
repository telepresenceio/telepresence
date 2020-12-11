package connector

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/datawire/ambassador/pkg/kates"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type DeploymentAction interface {
	do(dep *kates.Deployment) error
	isDone(dep *kates.Deployment) bool
	undo(dep *kates.Deployment) error
}

type ServiceAction interface {
	do(dep *kates.Service) error
	isDone(dep *kates.Service) bool
	undo(dep *kates.Service) error
}

func mustMarshal(data interface{}) string {
	js, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("internal error, unable to json.Marshal %T: %v", data, err))
	}
	return string(js)
}

// A makePortSymbolicAction replaces the numeric TargetPort of a ServicePort with a generated
// symbolic name so that an traffic-agent in a designated Deployment can reference the symbol
// and then use the original port number as the port to forward to when it is not intercepting.
type makePortSymbolicAction struct {
	PortName     string `json:"port_name"`
	TargetPort   int
	SymbolicName string
}

func (m *makePortSymbolicAction) getPort(svc *kates.Service, targetPort intstr.IntOrString) (*kates.ServicePort, error) {
	ports := svc.Spec.Ports
	for i := range ports {
		p := &ports[i]
		if p.TargetPort == targetPort {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unable to find target port %s in Service %s", targetPort.String(), svc.Name)
}

func (m *makePortSymbolicAction) do(svc *kates.Service) error {
	p, err := m.getPort(svc, intstr.FromInt(m.TargetPort))
	if err != nil {
		return err
	}
	m.SymbolicName = fmt.Sprintf("tel2px-%d", p.TargetPort.IntVal)
	p.TargetPort = intstr.FromString(m.SymbolicName)
	return nil
}

func (m *makePortSymbolicAction) isDone(svc *kates.Service) bool {
	_, err := m.getPort(svc, intstr.FromString(m.SymbolicName))
	return err == nil
}

func (m *makePortSymbolicAction) undo(svc *kates.Service) error {
	p, err := m.getPort(svc, intstr.FromString(m.SymbolicName))
	if err != nil {
		return err
	}
	p.TargetPort = intstr.FromInt(m.TargetPort)
	return nil
}

type svcActions struct {
	Version          string                  `json:"version"`
	MakePortSymbolic *makePortSymbolicAction `json:"make_port_symbolic,omitempty"`
}

func (s *svcActions) do(svc *kates.Service) error {
	if s.MakePortSymbolic != nil {
		return s.MakePortSymbolic.do(svc)
	}
	return nil
}

func (s *svcActions) isDone(svc *kates.Service) bool {
	if s.MakePortSymbolic != nil {
		return s.MakePortSymbolic.isDone(svc)
	}
	return false
}

func (s *svcActions) undo(svc *kates.Service) error {
	if s.MakePortSymbolic != nil {
		return s.MakePortSymbolic.undo(svc)
	}
	return nil
}

func (s *svcActions) String() string {
	return mustMarshal(s)
}

// addTrafficAgentAction is an action that adds a traffic-agent to the set of
// containers in a pod template spec.
type addTrafficAgentAction struct {
	ContainerPortName  string `json:"container_port_name"`
	ContainerPortProto string `json:"container_port_proto"`
	AppPort            int    `json:"app_port"`
}

func (ata *addTrafficAgentAction) do(dep *kates.Deployment) error {
	tplSpec := &dep.Spec.Template.Spec
	tplSpec.Containers = append(tplSpec.Containers, corev1.Container{
		Name:  "traffic-agent",
		Image: managerImageName(),
		Args:  []string{"agent"},
		Ports: []corev1.ContainerPort{{
			Name:          ata.ContainerPortName,
			Protocol:      corev1.Protocol(ata.ContainerPortProto),
			ContainerPort: 9900,
		}},
		Env: []corev1.EnvVar{{
			Name:  "LOG_LEVEL",
			Value: "debug",
		}, {
			Name:  "AGENT_NAME",
			Value: dep.Name,
		}, {
			Name:  "APP_PORT",
			Value: strconv.Itoa(ata.AppPort),
		}}})
	return nil
}

func (ata *addTrafficAgentAction) isDone(dep *kates.Deployment) bool {
	cns := dep.Spec.Template.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == "traffic-agent" {
			return true
		}
	}
	return false
}

func (ata *addTrafficAgentAction) undo(dep *kates.Deployment) error {
	cns := dep.Spec.Template.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == "traffic-agent" {
			// remove and keep order
			copy(cns[i:], cns[i+1:])
			last := len(cns) - 1
			cns[last] = kates.Container{}
			cns = cns[:last]
		}
	}
	return nil
}

// A hideContainerPortAction will replace the symbolic name of a container port
// with a generated name. It will perform the same replacement on all references
// to that port from the probes of the container
type hideContainerPortAction struct {
	ContainerName string `json:"container_name"`
	PortName      string `json:"port_name"`
	HiddenName    string `json:"hidden_name"`
}

func (hcp *hideContainerPortAction) getPort(dep *kates.Deployment, name string) (*kates.Container, *corev1.ContainerPort, error) {
	cns := dep.Spec.Template.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name != hcp.ContainerName {
			continue
		}
		ports := cn.Ports
		for pn := range ports {
			p := &ports[pn]
			if p.Name == name {
				return cn, p, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("unable to locate port %s in container %s in deployment %s", hcp.PortName, hcp.ContainerName, dep.Name)
}

func swapPortName(cn *kates.Container, p *corev1.ContainerPort, from, to string) {
	for _, probe := range []*corev1.Probe{cn.LivenessProbe, cn.ReadinessProbe, cn.StartupProbe} {
		if probe == nil {
			continue
		}
		if h := probe.HTTPGet; h != nil && h.Port.StrVal == from {
			h.Port.StrVal = to
		}
		if t := probe.TCPSocket; t != nil && t.Port.StrVal == from {
			t.Port.StrVal = to
		}
	}
	p.Name = to
}

func (hcp *hideContainerPortAction) do(dep *kates.Deployment) error {
	// New name must be max 15 characters long
	cn, p, err := hcp.getPort(dep, hcp.PortName)
	if err != nil {
		return err
	}
	hcp.HiddenName = fmt.Sprintf("tel2mv-%d", p.HostPort)
	swapPortName(cn, p, hcp.PortName, hcp.HiddenName)
	return nil
}

func (hcp *hideContainerPortAction) isDone(dep *kates.Deployment) bool {
	_, _, err := hcp.getPort(dep, hcp.HiddenName)
	return err == nil
}

func (hcp *hideContainerPortAction) undo(dep *kates.Deployment) error {
	cn, p, err := hcp.getPort(dep, hcp.HiddenName)
	if err != nil {
		return err
	}
	swapPortName(cn, p, hcp.HiddenName, hcp.PortName)
	return nil
}

type deploymentActions struct {
	Version           string                   `json:"version"`
	HideContainerPort *hideContainerPortAction `json:"hide_container_port,omitempty"`
	AddTrafficAgent   *addTrafficAgentAction   `json:"add_traffic_agent,omitempty"`
}

func (d *deploymentActions) do(dep *kates.Deployment) (err error) {
	if d.HideContainerPort != nil {
		if err = d.HideContainerPort.do(dep); err != nil {
			return err
		}
	}
	if d.AddTrafficAgent != nil {
		if err = d.AddTrafficAgent.do(dep); err != nil {
			return err
		}
	}
	return nil
}

func (d *deploymentActions) isDone(dep *kates.Deployment) bool {
	return (d.HideContainerPort == nil || d.HideContainerPort.isDone(dep)) &&
		(d.AddTrafficAgent == nil || d.AddTrafficAgent.isDone(dep))
}

func (d *deploymentActions) undo(dep *kates.Deployment) (err error) {
	if d.AddTrafficAgent != nil {
		if err = d.AddTrafficAgent.undo(dep); err != nil {
			return err
		}
	}
	if d.HideContainerPort != nil {
		if err = d.HideContainerPort.undo(dep); err != nil {
			return err
		}
	}
	return nil
}

func (d *deploymentActions) String() string {
	return mustMarshal(d)
}
