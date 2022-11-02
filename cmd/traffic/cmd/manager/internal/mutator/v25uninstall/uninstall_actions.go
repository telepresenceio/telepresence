package v25uninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/blang/semver"
	"github.com/hashicorp/go-multierror"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

// A partialAction is a single change that can be applied to an object.  A partialAction may not be
// applied by itself; it may only be applied as part of a larger completeAction.
type partialAction interface {
	// These are all Exported, so that you can easily tell which methods are implementing the
	// external interface and which are internal.

	Undo(ver semver.Version, obj k8sapi.Object) error

	ExplainUndo(obj k8sapi.Object, out io.Writer)
}

// A completeAction is a set of smaller partialActions that may be applied to an object.
type completeAction interface {
	Undo(obj k8sapi.Object) error
	ExplainUndo(obj k8sapi.Object, out io.Writer)

	// These are all Exported, so that you can easily tell which methods are implementing the
	// external interface and which are internal.

	UnmarshalAnnotation(string) error

	// TelVersion For actions-that-we-well-do, this is the currently running Telepresence version.  For
	// actions that we've read from in-cluster annotations, this is the Telepresence version
	// that originally performed the action.
	TelVersion() (semver.Version, error)
}

func nameAndNamespace(obj k8sapi.Object) string {
	mObj := obj.(meta.ObjectMetaAccessor).GetObjectMeta()
	return mObj.GetName() + "." + mObj.GetNamespace()
}

func explainUndo(c context.Context, a completeAction, obj k8sapi.Object) {
	var buf strings.Builder
	a.ExplainUndo(obj, &buf)
	if buf.Len() > 0 {
		dlog.Info(c, fmt.Sprintf("In %s %s, %s.",
			obj.GetKind(),
			nameAndNamespace(obj),
			buf.String()))
	}
}

// A multiAction combines zero-or-more partialActions together in to a single action.  This is
// useful as an internal implementation detail for implementing completeActions.
type multiAction []partialAction

func (ma multiAction) explain(
	obj k8sapi.Object,
	out io.Writer,
	ef func(partialAction partialAction, obj k8sapi.Object, out io.Writer),
) {
	for i, action := range ma {
		switch i {
		case 0:
			// nothing
		case len(ma) - 1:
			_, _ = io.WriteString(out, ", and ")
		default:
			_, _ = io.WriteString(out, ", ")
		}
		ef(action, obj, out)
	}
}

func (ma multiAction) ExplainUndo(obj k8sapi.Object, out io.Writer) {
	ma.explain(obj, out, partialAction.ExplainUndo)
}

func (ma multiAction) Undo(ver semver.Version, obj k8sapi.Object) error {
	var result *multierror.Error
	for i := len(ma) - 1; i >= 0; i-- {
		err := ma[i].Undo(ver, obj)
		result = multierror.Append(result, err)
	}
	return result.ErrorOrNil()
}

func unmarshalString(in string, out completeAction) error {
	return json.Unmarshal([]byte(in), out)
}

// A makePortSymbolicAction replaces the numeric TargetPort of a ServicePort with a generated
// symbolic name so that a traffic-agent in a designated Object can reference the symbol
// and then use the original port number as the port to forward to when it is not intercepting.
type makePortSymbolicAction struct {
	PortName     string
	TargetPort   uint16
	SymbolicName string
}

var _ partialAction = (*makePortSymbolicAction)(nil)

func (m *makePortSymbolicAction) portName(port string) string {
	if m.PortName == "" {
		return port
	}
	return m.PortName + "." + port
}

func (m *makePortSymbolicAction) getPort(o k8sapi.Object, targetPort intstr.IntOrString) (*core.ServicePort, error) {
	svc, ok := k8sapi.ServiceImpl(o)
	if !ok {
		return nil, k8sapi.ObjErrorf(o, "not a Service")
	}
	ports := svc.Spec.Ports
	for i := range ports {
		p := &ports[i]
		if p.TargetPort == targetPort && p.Name == m.PortName {
			return p, nil
		}
	}
	return nil, k8sapi.ObjErrorf(o, "unable to find target port %q",
		m.portName(targetPort.String()))
}

func (m *makePortSymbolicAction) ExplainUndo(_ k8sapi.Object, out io.Writer) {
	fmt.Fprintf(out, "restore symbolic service port %s to numeric %d",
		m.portName(m.SymbolicName), m.TargetPort)
}

func (m *makePortSymbolicAction) Undo(ver semver.Version, svc k8sapi.Object) error {
	p, err := m.getPort(svc, intstr.FromString(m.SymbolicName))
	if err != nil {
		return install.NewAlreadyUndone(err, "symbolic port has already been removed")
	}
	p.TargetPort = intstr.FromInt(int(m.TargetPort))
	return nil
}

// addSymbolicPortAction ///////////////////////////////////////////////////////

// An addSymbolicPortAction is like makeSymbolicPortAction but instead of replacing a TargetPort, it adds one.
// This is for the case where the service doesn't declare a TargetPort but instead relies on that
// it defaults to the Port.
type addSymbolicPortAction struct {
	makePortSymbolicAction
}

var _ partialAction = (*addSymbolicPortAction)(nil)

func (m *addSymbolicPortAction) ExplainUndo(_ k8sapi.Object, out io.Writer) {
	fmt.Fprintf(out, "remove symbolic service port %s", m.portName(m.SymbolicName))
}

func (m *addSymbolicPortAction) Undo(ver semver.Version, svc k8sapi.Object) error {
	p, err := m.makePortSymbolicAction.getPort(svc, intstr.FromString(m.SymbolicName))
	if err != nil {
		return install.NewAlreadyUndone(err, "symbolic port has already been removed")
	}
	p.TargetPort = intstr.IntOrString{}
	return nil
}

// svcActions //////////////////////////////////////////////////////////////////

type svcActions struct {
	Version          string                  `json:"version"`
	MakePortSymbolic *makePortSymbolicAction `json:"make_port_symbolic,omitempty"`
	AddSymbolicPort  *addSymbolicPortAction  `json:"add_symbolic_port,omitempty"`
}

var _ completeAction = (*svcActions)(nil)

func (s *svcActions) actions() (actions multiAction) {
	if s.MakePortSymbolic != nil {
		actions = append(actions, s.MakePortSymbolic)
	}
	if s.AddSymbolicPort != nil {
		actions = append(actions, s.AddSymbolicPort)
	}
	return actions
}

func (s *svcActions) ExplainUndo(svc k8sapi.Object, out io.Writer) {
	s.actions().ExplainUndo(svc, out)
}

func (s *svcActions) Undo(svc k8sapi.Object) (err error) {
	ver, err := s.TelVersion()
	if err != nil {
		return err
	}
	return s.actions().Undo(ver, svc)
}

func (s *svcActions) UnmarshalAnnotation(str string) error {
	return unmarshalString(str, s)
}

func (s *svcActions) TelVersion() (semver.Version, error) {
	return semver.Parse(s.Version)
}

// addTrafficAgentAction ///////////////////////////////////////////////////////

// addTrafficAgentAction is a partialAction that adds a traffic-agent to the set of containers in a
// pod template spec.
type addTrafficAgentAction struct {
	// The information of the pre-existing container port that the agent will take over.
	ContainerPortName     string        `json:"container_port_name"`
	ContainerPortProto    core.Protocol `json:"container_port_proto"`
	ContainerPortAppProto string        `json:"container_port_app_proto,omitempty"`
	ContainerPortNumber   uint16        `json:"app_port"`
	APIPortNumber         uint16        `json:"api_port,omitempty"`

	// The image name of the agent to add
	ImageName string `json:"image_name"`
}

var _ partialAction = (*addTrafficAgentAction)(nil)

func (ata *addTrafficAgentAction) ExplainUndo(_ k8sapi.Object, out io.Writer) {
	fmt.Fprintf(out, "remove traffic-agent container with image %s", ata.ImageName)
}

func (ata *addTrafficAgentAction) dropAgentAnnotationVolume(obj k8sapi.Object, tplSpec *core.PodTemplateSpec) error {
	volumeIdx := -1
	for i := range tplSpec.Spec.Volumes {
		if tplSpec.Spec.Volumes[i].Name == install.AgentAnnotationVolumeName {
			volumeIdx = i
			break
		}
	}

	if volumeIdx < 0 {
		return install.NewAlreadyUndone(k8sapi.ObjErrorf(obj, "does not contain a %q volume", install.AgentAnnotationVolumeName), "cannot delete volume")
	}
	if len(tplSpec.Spec.Volumes) == 1 {
		tplSpec.Spec.Volumes = nil
	} else {
		tplSpec.Spec.Volumes = append(tplSpec.Spec.Volumes[:volumeIdx], tplSpec.Spec.Volumes[volumeIdx+1:]...)
	}
	return nil
}

func (ata *addTrafficAgentAction) Undo(ver semver.Version, obj k8sapi.Object) error {
	tplSpec := obj.(k8sapi.Workload).GetPodTemplate()
	containerIdx := -1
	for i := range tplSpec.Spec.Containers {
		if tplSpec.Spec.Containers[i].Name == install.AgentContainerName {
			containerIdx = i
			break
		}
	}
	if containerIdx < 0 {
		return install.NewAlreadyUndone(k8sapi.ObjErrorf(obj, "does not contain a %q container", install.AgentContainerName), "cannot undo agent container")
	}
	tplSpec.Spec.Containers = append(tplSpec.Spec.Containers[:containerIdx], tplSpec.Spec.Containers[containerIdx+1:]...)

	if ver.GE(semver.MustParse("2.1.5")) {
		err := ata.dropAgentAnnotationVolume(obj, tplSpec)
		if err != nil {
			return err
		}
	}

	return nil
}

// addInitContainerAction ///////////////////////////////////////////////////////

// addInitContainerAction is a partialAction that adds a traffic-agent to the set of containers in a
// pod template spec.
type addInitContainerAction struct {
	// The information of the pre-existing container port that the agent will take over.
	AppPortProto  core.Protocol `json:"container_port_proto"`
	AppPortNumber uint16        `json:"app_port"`

	// The image name of the initContainer to add -- usually the same as the traffic agent image that will be used
	ImageName string `json:"image_name"`
}

var _ partialAction = (*addInitContainerAction)(nil)

func (ica *addInitContainerAction) ExplainUndo(_ k8sapi.Object, out io.Writer) {
	fmt.Fprintf(out, "remove %s initContainer with image %s", install.InitContainerName, ica.ImageName)
}

func (ica *addInitContainerAction) Undo(ver semver.Version, obj k8sapi.Object) error {
	tplSpec := obj.(k8sapi.Workload).GetPodTemplate()
	containerIdx := -1
	cns := tplSpec.Spec.InitContainers
	if cns == nil {
		return install.NewAlreadyUndone(k8sapi.ObjErrorf(obj, "does not contain a %q initContainer", install.InitContainerName), "cannot undo initContainer")
	}
	for i := range cns {
		if tplSpec.Spec.Containers[i].Name == install.InitContainerName {
			containerIdx = i
			break
		}
	}
	if containerIdx < 0 {
		return install.NewAlreadyUndone(k8sapi.ObjErrorf(obj, "does not contain a %q initContainer", install.InitContainerName), "cannot undo initContainer")
	}
	tplSpec.Spec.InitContainers = append(tplSpec.Spec.InitContainers[:containerIdx], tplSpec.Spec.InitContainers[containerIdx+1:]...)
	return nil
}

// addTPEnvironmentAction  /////////////////////////////////////////////////////.
type addTPEnvironmentAction struct {
	ContainerName string `json:"container_name"`
	Env           map[string]string
}

func (ae *addTPEnvironmentAction) Undo(_ semver.Version, obj k8sapi.Object) error {
	cn, err := ae.getContainer(obj)
	if err != nil {
		return err
	}
	cEnv := make([]core.EnvVar, 0, len(cn.Env))
	for _, env := range cn.Env {
		if _, ok := ae.Env[env.Name]; !ok {
			cEnv = append(cEnv, env)
		}
	}
	if len(cEnv) == 0 {
		cEnv = nil
	}
	cn.Env = cEnv
	return nil
}

func (ae *addTPEnvironmentAction) ExplainUndo(_ k8sapi.Object, out io.Writer) {
	fmt.Fprintf(out, "remove environment %v from container %s", ae.Env, ae.ContainerName)
}

func (ae *addTPEnvironmentAction) getContainer(obj k8sapi.Object) (*core.Container, error) {
	tplSpec := obj.(k8sapi.Workload).GetPodTemplate()
	cns := tplSpec.Spec.Containers
	for i := range cns {
		if cn := &cns[i]; cn.Name == ae.ContainerName {
			return cn, nil
		}
	}
	return nil, k8sapi.ObjErrorf(obj, "does not contain a %q container", ae.ContainerName)
}

// hideContainerPortAction /////////////////////////////////////////////////////

// A hideContainerPortAction will replace the symbolic name of a container port
// with a generated name. It will perform the same replacement on all references
// to that port from the probes of the container.
type hideContainerPortAction struct {
	ContainerName string `json:"container_name"`
	PortName      string `json:"port_name"`
	// HiddenName is the name that we swapped it to; this is set by Do(), and read by Undo().
	HiddenName string `json:"hidden_name"`
}

var _ partialAction = (*hideContainerPortAction)(nil)

func (hcp *hideContainerPortAction) getPort(obj k8sapi.Object, name string) (*core.Container, *core.ContainerPort, error) {
	tplSpec := obj.(k8sapi.Workload).GetPodTemplate()
	cns := tplSpec.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name != hcp.ContainerName {
			continue
		}
		p, err := k8sapi.GetPort(cn, name)
		if err != nil {
			return nil, nil, k8sapi.ObjErrorf(obj, err.Error())
		}
		return cn, p, nil
	}
	return nil, nil, k8sapi.ObjErrorf(obj, "unable to locate container %q", hcp.ContainerName)
}

func swapPortName(cn *core.Container, p *core.ContainerPort, from, to string) {
	for _, probe := range []*core.Probe{cn.LivenessProbe, cn.ReadinessProbe, cn.StartupProbe} {
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

func (hcp *hideContainerPortAction) ExplainUndo(_ k8sapi.Object, out io.Writer) {
	fmt.Fprintf(out, "reveal hidden port %q in container %s by restoring its origina name %q",
		hcp.HiddenName, hcp.ContainerName, hcp.PortName)
}

func (hcp *hideContainerPortAction) Undo(ver semver.Version, obj k8sapi.Object) error {
	return hcp.undo(obj)
}

func (hcp *hideContainerPortAction) undo(obj k8sapi.Object) error {
	cn, p, err := hcp.getPort(obj, hcp.HiddenName)
	if err != nil {
		return err
	}
	swapPortName(cn, p, hcp.HiddenName, hcp.PortName)
	return nil
}

// workloadActions ///////////////////////////////////////////////////////////.
type workloadActions struct {
	Version                   string `json:"version"`
	ReferencedService         string
	ReferencedServicePort     string                   `json:"referenced_service_port,omitempty"`
	ReferencedServicePortName string                   `json:"referenced_service_port_name,omitempty"`
	HideContainerPort         *hideContainerPortAction `json:"hide_container_port,omitempty"`
	AddTrafficAgent           *addTrafficAgentAction   `json:"add_traffic_agent,omitempty"`
	AddInitContainer          *addInitContainerAction  `json:"add_init_container,omitempty"`
	AddTPEnvironmentAction    *addTPEnvironmentAction  `json:"add_tp_env,omitempty"`
}

var _ completeAction = (*workloadActions)(nil)

func (d *workloadActions) actions() (actions multiAction) {
	if d.HideContainerPort != nil {
		actions = append(actions, d.HideContainerPort)
	}
	if d.AddTrafficAgent != nil {
		actions = append(actions, d.AddTrafficAgent)
	}
	if d.AddInitContainer != nil {
		actions = append(actions, d.AddInitContainer)
	}
	if d.AddTPEnvironmentAction != nil {
		actions = append(actions, d.AddTPEnvironmentAction)
	}
	return actions
}

func (d *workloadActions) ExplainUndo(dep k8sapi.Object, out io.Writer) {
	d.actions().ExplainUndo(dep, out)
}

func (d *workloadActions) Undo(dep k8sapi.Object) (err error) {
	ver, err := d.TelVersion()
	if err != nil {
		return err
	}
	return d.actions().Undo(ver, dep)
}

func (d *workloadActions) UnmarshalAnnotation(str string) error {
	return unmarshalString(str, d)
}

func (d *workloadActions) TelVersion() (semver.Version, error) {
	return semver.Parse(d.Version)
}
