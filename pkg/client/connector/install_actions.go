package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blang/semver"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
)

// Public interface-y pieces ///////////////////////////////////////////////////

// A partialAction is a single change that can be applied to an object.  A partialAction may not be
// applied by itself; it may only be applied as part of a larger completeAction.
type partialAction interface {
	// These are all Exported, so that you can easily tell which methods are implementing the
	// external interface and which are internal.

	Do(obj kates.Object) error
	Undo(ver semver.Version, obj kates.Object) error

	ExplainDo(obj kates.Object, out io.Writer)
	ExplainUndo(obj kates.Object, out io.Writer)

	IsDone(obj kates.Object) bool
}

// A completeAction is a set of smaller partialActions that may be applied to an object.
type completeAction interface {
	// These five methods are the same as partialAction, except 'Undo' is different.
	Do(obj kates.Object) error
	Undo(obj kates.Object) error
	ExplainDo(obj kates.Object, out io.Writer)
	ExplainUndo(obj kates.Object, out io.Writer)
	IsDone(obj kates.Object) bool

	// These are all Exported, so that you can easily tell which methods are implementing the
	// external interface and which are internal.

	MarshalAnnotation() (string, error)
	UnmarshalAnnotation(string) error

	// For actions-that-we-well-do, this is the currently running Telepresence version.  For
	// actions that we've read from in-cluster annotations, this is the Telepresence version
	// that originally performed the action.
	TelVersion() (semver.Version, error)
}

func explainDo(c context.Context, a completeAction, obj kates.Object) {
	var buf strings.Builder
	a.ExplainDo(obj, &buf)
	if buf.Len() > 0 {
		dlog.Info(c, fmt.Sprintf("In %s %s, %s.",
			obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetName(),
			buf.String()))
	}
}

func explainUndo(c context.Context, a completeAction, obj kates.Object) {
	var buf strings.Builder
	a.ExplainUndo(obj, &buf)
	if buf.Len() > 0 {
		dlog.Info(c, fmt.Sprintf("In %s %s, %s.",
			obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetName(),
			buf.String()))
	}
}

// multiAction /////////////////////////////////////////////////////////////////

// A multiAction combines zero-or-more partialActions together in to a single action.  This is
// useful as an internal implementation detail for implementing completeActions.
type multiAction []partialAction

func (ma multiAction) explain(
	obj kates.Object,
	out io.Writer,
	ef func(partialAction partialAction, obj kates.Object, out io.Writer),
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

func (ma multiAction) ExplainDo(obj kates.Object, out io.Writer) {
	ma.explain(obj, out, partialAction.ExplainDo)
}

func (ma multiAction) ExplainUndo(obj kates.Object, out io.Writer) {
	ma.explain(obj, out, partialAction.ExplainUndo)
}

func (ma multiAction) Do(obj kates.Object) error {
	for _, partialAction := range ma {
		if err := partialAction.Do(obj); err != nil {
			return err
		}
	}
	return nil
}

func (ma multiAction) IsDone(obj kates.Object) bool {
	for _, partialAction := range ma {
		if !partialAction.IsDone(obj) {
			return false
		}
	}
	return true
}

func (ma multiAction) Undo(ver semver.Version, obj kates.Object) error {
	for i := len(ma) - 1; i >= 0; i-- {
		if err := ma[i].Undo(ver, obj); err != nil {
			return err
		}
	}
	return nil
}

// Internal convenience functions //////////////////////////////////////////////

func marshalString(data completeAction) (string, error) {
	js, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(js), nil
}

func unmarshalString(in string, out completeAction) error {
	return json.Unmarshal([]byte(in), out)
}

func GetPodTemplateFromObject(obj kates.Object) (*kates.PodTemplateSpec, error) {
	var tplSpec *kates.PodTemplateSpec
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	switch kind {
	case "ReplicaSet":
		rs := obj.(*kates.ReplicaSet)
		tplSpec = &rs.Spec.Template
	case "Deployment":
		dep := obj.(*kates.Deployment)
		tplSpec = &dep.Spec.Template
	case "StatefulSet":
		statefulSet := obj.(*kates.StatefulSet)
		tplSpec = &statefulSet.Spec.Template
	default:
		return nil, objErrorf(obj, "unsupported workload kind %q", kind)
	}

	return tplSpec, nil
}

// A makePortSymbolicAction replaces the numeric TargetPort of a ServicePort with a generated
// symbolic name so that an traffic-agent in a designated Workload can reference the symbol
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

func (m *makePortSymbolicAction) getPort(svc kates.Object, targetPort intstr.IntOrString) (*kates.ServicePort, error) {
	ports := svc.(*kates.Service).Spec.Ports
	for i := range ports {
		p := &ports[i]
		if p.TargetPort == targetPort && p.Name == m.PortName {
			return p, nil
		}
	}
	return nil, objErrorf(svc, "unable to find target port %q",
		m.portName(targetPort.String()))
}

func (m *makePortSymbolicAction) Do(svc kates.Object) error {
	p, err := m.getPort(svc, intstr.FromInt(int(m.TargetPort)))
	if err != nil {
		return err
	}
	p.TargetPort = intstr.FromString(m.SymbolicName)
	return nil
}

func (m *makePortSymbolicAction) ExplainDo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "make service port %s symbolic with name %q",
		m.portName(strconv.Itoa(int(m.TargetPort))), m.SymbolicName)
}

func (m *makePortSymbolicAction) ExplainUndo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "restore symbolic service port %s to numeric %d",
		m.portName(m.SymbolicName), m.TargetPort)
}

func (m *makePortSymbolicAction) IsDone(svc kates.Object) bool {
	_, err := m.getPort(svc, intstr.FromString(m.SymbolicName))
	return err == nil
}

func (m *makePortSymbolicAction) Undo(ver semver.Version, svc kates.Object) error {
	p, err := m.getPort(svc, intstr.FromString(m.SymbolicName))
	if err != nil {
		return err
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

func (m *addSymbolicPortAction) getPort(svc kates.Object, targetPort int32) (*kates.ServicePort, error) {
	ports := svc.(*kates.Service).Spec.Ports
	for i := range ports {
		p := &ports[i]
		if p.TargetPort.Type == intstr.Int && p.TargetPort.IntVal == 0 && p.Port == targetPort {
			// p.TargetPort is not set, so default to p.Port
			return p, nil
		}
	}
	return nil, objErrorf(svc, "unable to find port %d", targetPort)
}

func (m *addSymbolicPortAction) ExplainDo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "add targetPort to service port %s symbolic with name %q",
		m.portName(strconv.Itoa(int(m.TargetPort))), m.SymbolicName)
}

func (m *addSymbolicPortAction) Do(svc kates.Object) error {
	p, err := m.getPort(svc, int32(m.TargetPort))
	if err != nil {
		return err
	}
	p.TargetPort = intstr.FromString(m.SymbolicName)
	return nil
}

func (m *addSymbolicPortAction) ExplainUndo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "remove symbolic service port %s", m.portName(m.SymbolicName))
}

func (m *addSymbolicPortAction) Undo(ver semver.Version, svc kates.Object) error {
	p, err := m.makePortSymbolicAction.getPort(svc, intstr.FromString(m.SymbolicName))
	if err != nil {
		return err
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

func (s *svcActions) Do(svc kates.Object) (err error) {
	return s.actions().Do(svc)
}

func (s *svcActions) ExplainDo(svc kates.Object, out io.Writer) {
	s.actions().ExplainDo(svc, out)
}

func (s *svcActions) ExplainUndo(svc kates.Object, out io.Writer) {
	s.actions().ExplainUndo(svc, out)
}

func (s *svcActions) IsDone(svc kates.Object) bool {
	return s.actions().IsDone(svc)
}

func (s *svcActions) Undo(svc kates.Object) (err error) {
	ver, err := s.TelVersion()
	if err != nil {
		return err
	}
	return s.actions().Undo(ver, svc)
}

func (s *svcActions) MarshalAnnotation() (string, error) {
	return marshalString(s)
}

func (s *svcActions) UnmarshalAnnotation(str string) error {
	return unmarshalString(str, s)
}

func (s *svcActions) TelVersion() (semver.Version, error) {
	return semver.Parse(s.Version)
}

// addTrafficAgentAction ///////////////////////////////////////////////////////

const (
	envPrefix        = "TEL_APP_"
	telAppMountPoint = "/tel_app_mounts"
)

// addTrafficAgentAction is a partialAction that adds a traffic-agent to the set of containers in a
// pod template spec.
type addTrafficAgentAction struct {
	// The information of the pre-existing container port that the agent will take over.
	ContainerPortName   string          `json:"container_port_name"`
	ContainerPortProto  corev1.Protocol `json:"container_port_proto"`
	ContainerPortNumber uint16          `json:"app_port"`

	// The image name of the agent to add
	ImageName string `json:"image_name"`

	// The name of the app container. Not exported because its not needed for undo.
	containerName string
}

var _ partialAction = (*addTrafficAgentAction)(nil)

func (ata *addTrafficAgentAction) appContainer(cns []kates.Container) *kates.Container {
	for i := range cns {
		cn := &cns[i]
		if cn.Name == ata.containerName {
			return cn
		}
	}
	return nil
}

func (ata *addTrafficAgentAction) Do(obj kates.Object) error {
	tplSpec, err := GetPodTemplateFromObject(obj)
	if err != nil {
		return err
	}
	cns := tplSpec.Spec.Containers
	appContainer := ata.appContainer(cns)
	if appContainer == nil {
		return objErrorf(obj, "unable to find app container %q in", ata.containerName)
	}

	tplSpec.Spec.Volumes = append(tplSpec.Spec.Volumes, corev1.Volume{
		Name: agentAnnotationVolumeName,
		VolumeSource: corev1.VolumeSource{
			DownwardAPI: &corev1.DownwardAPIVolumeSource{
				Items: []corev1.DownwardAPIVolumeFile{
					{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.annotations",
						},
						Path: "annotations",
					},
				},
			},
		},
	})
	tplSpec.Spec.Containers = append(tplSpec.Spec.Containers, corev1.Container{
		Name:  agentContainerName,
		Image: ata.ImageName,
		Args:  []string{"agent"},
		Ports: []corev1.ContainerPort{{
			Name:          ata.ContainerPortName,
			Protocol:      ata.ContainerPortProto,
			ContainerPort: 9900,
		}},
		Env:          ata.agentEnvironment(obj.GetName(), appContainer),
		EnvFrom:      ata.agentEnvFrom(appContainer.EnvFrom),
		VolumeMounts: ata.agentVolumeMounts(appContainer.VolumeMounts),
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	})
	return nil
}

func (ata *addTrafficAgentAction) agentEnvFrom(appEF []corev1.EnvFromSource) []corev1.EnvFromSource {
	if ln := len(appEF); ln > 0 {
		agentEF := make([]corev1.EnvFromSource, ln)
		for i, appE := range appEF {
			appE.Prefix = envPrefix + appE.Prefix
			agentEF[i] = appE
		}
		return agentEF
	}
	return appEF
}

func (ata *addTrafficAgentAction) agentEnvironment(agentName string, appContainer *kates.Container) []corev1.EnvVar {
	appEnv := ata.appEnvironment(appContainer)
	env := make([]corev1.EnvVar, len(appEnv), len(appEnv)+7)
	copy(env, appEnv)
	env = append(env,
		corev1.EnvVar{
			Name:  "LOG_LEVEL",
			Value: "debug",
		},
		corev1.EnvVar{
			Name:  "AGENT_NAME",
			Value: agentName,
		},
		corev1.EnvVar{
			Name: "AGENT_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		corev1.EnvVar{
			Name: "AGENT_POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		corev1.EnvVar{
			Name:  "APP_PORT",
			Value: strconv.Itoa(int(ata.ContainerPortNumber)),
		})
	if len(appContainer.VolumeMounts) > 0 {
		env = append(env, corev1.EnvVar{
			Name:  "APP_MOUNTS",
			Value: telAppMountPoint,
		})

		// Have the agent propagate the mount-points as TELEPRESENCE_MOUNTS to make it easy for the
		// local app to create symlinks.
		mounts := make([]string, len(appContainer.VolumeMounts))
		for i := range appContainer.VolumeMounts {
			mounts[i] = appContainer.VolumeMounts[i].MountPath
		}
		env = append(env, corev1.EnvVar{
			Name:  envPrefix + "TELEPRESENCE_MOUNTS",
			Value: strings.Join(mounts, ":"),
		})
	}
	env = append(env, corev1.EnvVar{
		Name:  "MANAGER_HOST",
		Value: managerAppName + "." + managerNamespace,
	})
	return env
}

func (ata *addTrafficAgentAction) agentVolumeMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	agentMounts := make([]corev1.VolumeMount, len(mounts)+1)
	for i, mount := range mounts {
		mount.MountPath = filepath.Join(telAppMountPoint, mount.MountPath)
		agentMounts[i] = mount
	}
	agentMounts[len(mounts)] = corev1.VolumeMount{
		Name:      agentAnnotationVolumeName,
		MountPath: "/tel_pod_info",
	}
	return agentMounts
}

func (ata *addTrafficAgentAction) appEnvironment(appContainer *kates.Container) []corev1.EnvVar {
	envCopy := make([]corev1.EnvVar, len(appContainer.Env)+1)
	for i, ev := range appContainer.Env {
		ev.Name = envPrefix + ev.Name
		envCopy[i] = ev
	}
	envCopy[len(appContainer.Env)] = corev1.EnvVar{
		Name:  "TELEPRESENCE_CONTAINER",
		Value: appContainer.Name,
	}
	return envCopy
}

func (ata *addTrafficAgentAction) ExplainDo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "add traffic-agent container with image %s", ata.ImageName)
}

func (ata *addTrafficAgentAction) ExplainUndo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "remove traffic-agent container with image %s", ata.ImageName)
}

func (ata *addTrafficAgentAction) IsDone(obj kates.Object) bool {
	tplSpec, err := GetPodTemplateFromObject(obj)
	if err != nil {
		return false
	}
	cns := tplSpec.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agentContainerName {
			return true
		}
	}
	return false
}

func (ata *addTrafficAgentAction) Undo(ver semver.Version, obj kates.Object) error {
	tplSpec, err := GetPodTemplateFromObject(obj)
	if err != nil {
		return err
	}

	containerIdx := -1
	for i := range tplSpec.Spec.Containers {
		if tplSpec.Spec.Containers[i].Name == agentContainerName {
			containerIdx = i
			break
		}
	}
	if containerIdx < 0 {
		return objErrorf(obj, "does not contain a %q container", agentContainerName)
	}
	tplSpec.Spec.Containers = append(tplSpec.Spec.Containers[:containerIdx], tplSpec.Spec.Containers[containerIdx+1:]...)

	if ver.GE(semver.MustParse("2.1.5")) {
		volumeIdx := -1
		for i := range tplSpec.Spec.Volumes {
			if tplSpec.Spec.Volumes[i].Name == agentAnnotationVolumeName {
				volumeIdx = i
				break
			}
		}

		if volumeIdx < 0 {
			return objErrorf(obj, "does not contain a %q volume", agentAnnotationVolumeName)
		}
		if len(tplSpec.Spec.Volumes) == 1 {
			tplSpec.Spec.Volumes = nil
		} else {
			tplSpec.Spec.Volumes = append(tplSpec.Spec.Volumes[:volumeIdx], tplSpec.Spec.Volumes[volumeIdx+1:]...)
		}
	}

	return nil
}

// hideContainerPortAction /////////////////////////////////////////////////////

// A hideContainerPortAction will replace the symbolic name of a container port
// with a generated name. It will perform the same replacement on all references
// to that port from the probes of the container
type hideContainerPortAction struct {
	ContainerName string `json:"container_name"`
	PortName      string `json:"port_name"`
	// HiddenName is the name that we swapped it to; this is set by Do(), and read by Undo().
	HiddenName string `json:"hidden_name"`

	// ordinal is only used for avoiding ambiguities when generating the HiddenName. It
	// is the zero based order of all hideContainerPortAction instances for a workload.
	// Right now we only use one port so this is always zero.
	ordinal int
}

var _ partialAction = (*hideContainerPortAction)(nil)

func (hcp *hideContainerPortAction) getPort(obj kates.Object, name string) (*kates.Container, *corev1.ContainerPort, error) {
	tplSpec, err := GetPodTemplateFromObject(obj)
	if err != nil {
		return nil, nil, err
	}
	cns := tplSpec.Spec.Containers
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
	return nil, nil, objErrorf(obj, "unable to locate port %q in container %q", name, hcp.ContainerName)
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

func (hcp *hideContainerPortAction) Do(obj kates.Object) error {
	return hcp.do(obj)
}

const maxPortNameLen = 15

func (hcp *hideContainerPortAction) do(obj kates.Object) error {
	cn, p, err := hcp.getPort(obj, hcp.PortName)
	if err != nil {
		return err
	}

	// New name must be max 15 characters long
	hcp.HiddenName = "tm-" + p.Name
	if len(hcp.HiddenName) > maxPortNameLen {
		if hcp.ordinal > 0 {
			hcp.HiddenName = hcp.HiddenName[:maxPortNameLen-2] + strconv.FormatInt(int64(hcp.ordinal), 16) // we don't expect more than 256 ports
		} else {
			hcp.HiddenName = hcp.HiddenName[:maxPortNameLen]
		}
	}
	swapPortName(cn, p, hcp.PortName, hcp.HiddenName)
	return nil
}

func (hcp *hideContainerPortAction) ExplainDo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "hide port %q in container %s from service by renaming it to %q",
		hcp.PortName, hcp.ContainerName, hcp.HiddenName)
}

func (hcp *hideContainerPortAction) ExplainUndo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "reveal hidden port %q in container %s by restoring its origina name %q",
		hcp.HiddenName, hcp.ContainerName, hcp.PortName)
}

func (hcp *hideContainerPortAction) IsDone(obj kates.Object) bool {
	_, _, err := hcp.getPort(obj, hcp.HiddenName)
	return err == nil
}

func (hcp *hideContainerPortAction) Undo(ver semver.Version, obj kates.Object) error {
	return hcp.undo(obj)
}

func (hcp *hideContainerPortAction) undo(obj kates.Object) error {
	cn, p, err := hcp.getPort(obj, hcp.HiddenName)
	if err != nil {
		return err
	}
	swapPortName(cn, p, hcp.HiddenName, hcp.PortName)
	return nil
}

// workloadActions ///////////////////////////////////////////////////////////

type workloadActions struct {
	Version                   string `json:"version"`
	ReferencedService         string
	ReferencedServicePort     string                   `json:"referenced_service_port,omitempty"`
	ReferencedServicePortName string                   `json:"referenced_service_port_name,omitempty"`
	HideContainerPort         *hideContainerPortAction `json:"hide_container_port,omitempty"`
	AddTrafficAgent           *addTrafficAgentAction   `json:"add_traffic_agent,omitempty"`
}

var _ completeAction = (*workloadActions)(nil)

func (d *workloadActions) actions() (actions multiAction) {
	if d.HideContainerPort != nil {
		actions = append(actions, d.HideContainerPort)
	}
	if d.AddTrafficAgent != nil {
		actions = append(actions, d.AddTrafficAgent)
	}
	return actions
}

func (d *workloadActions) ExplainDo(dep kates.Object, out io.Writer) {
	d.actions().ExplainDo(dep, out)
}

func (d *workloadActions) Do(dep kates.Object) (err error) {
	return d.actions().Do(dep)
}

func (d *workloadActions) ExplainUndo(dep kates.Object, out io.Writer) {
	d.actions().ExplainUndo(dep, out)
}

func (d *workloadActions) IsDone(dep kates.Object) bool {
	return d.actions().IsDone(dep)
}

func (d *workloadActions) Undo(dep kates.Object) (err error) {
	ver, err := d.TelVersion()
	if err != nil {
		return err
	}
	return d.actions().Undo(ver, dep)
}

func (d *workloadActions) MarshalAnnotation() (string, error) {
	return marshalString(d)
}

func (d *workloadActions) UnmarshalAnnotation(str string) error {
	return unmarshalString(str, d)
}

func (d *workloadActions) TelVersion() (semver.Version, error) {
	return semver.Parse(d.Version)
}
