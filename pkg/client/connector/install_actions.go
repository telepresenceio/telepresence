package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
)

const telAppMountPoint = "/tel_app_mounts"

type action interface {
	do(obj kates.Object) error
	explainDo(obj kates.Object, out io.Writer)
	explainUndo(obj kates.Object, out io.Writer)
	isDone(obj kates.Object) bool
	undo(obj kates.Object) error
}

type multiAction interface {
	action
	version() string
	actions() []action
	objectType() string
}

type explainFunc func(action action, obj kates.Object, out io.Writer)

func explainDo(c context.Context, a action, obj kates.Object) {
	explainAction(c, a, obj, action.explainDo)
}

func explainUndo(c context.Context, a action, obj kates.Object) {
	explainAction(c, a, obj, action.explainUndo)
}

func explainAction(c context.Context, a action, obj kates.Object, ef explainFunc) {
	buf := bytes.Buffer{}
	if ma, ok := a.(multiAction); ok {
		explainActions(ma, obj, ef, &buf)
	} else {
		ef(a, obj, &buf)
	}
	if bts := buf.Bytes(); len(bts) > 0 {
		dlog.Info(c, string(bts))
	}
}

func explainActions(ma multiAction, obj kates.Object, ef explainFunc, out io.Writer) {
	actions := ma.actions()
	last := len(actions) - 1
	if last < 0 {
		return
	}
	fmt.Fprintf(out, "In %s %s, ", ma.objectType(), obj.GetName())

	switch last {
	case 0:
	case 1:
		ef(actions[0], obj, out)
		fmt.Fprint(out, " and ")
	default:
		for _, action := range actions[:last] {
			ef(action, obj, out)
			fmt.Fprint(out, ", ")
		}
		fmt.Fprint(out, "and ")
	}
	ef(actions[last], obj, out)
	fmt.Fprint(out, ".")
}

func doActions(ma multiAction, obj kates.Object) (err error) {
	for _, action := range ma.actions() {
		if err = action.do(obj); err != nil {
			return err
		}
	}
	return nil
}

func isDoneActions(ma multiAction, obj kates.Object) bool {
	for _, action := range ma.actions() {
		if !action.isDone(obj) {
			return false
		}
	}
	return true
}

func undoActions(ma multiAction, obj kates.Object) (err error) {
	actions := ma.actions()
	for i := len(actions) - 1; i >= 0; i-- {
		if err = actions[i].undo(obj); err != nil {
			return err
		}
	}
	return nil
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
	PortName     string
	TargetPort   uint16
	SymbolicName string
}

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
	return nil, fmt.Errorf("unable to find target port %s in Service %s",
		m.portName(targetPort.String()), svc.GetName())
}

func (m *makePortSymbolicAction) do(svc kates.Object) error {
	p, err := m.getPort(svc, intstr.FromInt(int(m.TargetPort)))
	if err != nil {
		return err
	}
	p.TargetPort = intstr.FromString(m.SymbolicName)
	return nil
}

func (m *makePortSymbolicAction) explainDo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "make service port %s symbolic with name %q",
		m.portName(strconv.Itoa(int(m.TargetPort))), m.SymbolicName)
}

func (m *makePortSymbolicAction) explainUndo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "restore symbolic service port %s to numeric %d",
		m.portName(m.SymbolicName), m.TargetPort)
}

func (m *makePortSymbolicAction) isDone(svc kates.Object) bool {
	_, err := m.getPort(svc, intstr.FromString(m.SymbolicName))
	return err == nil
}

func (m *makePortSymbolicAction) undo(svc kates.Object) error {
	p, err := m.getPort(svc, intstr.FromString(m.SymbolicName))
	if err != nil {
		return err
	}
	p.TargetPort = intstr.FromInt(int(m.TargetPort))
	return nil
}

type svcActions struct {
	Version          string                  `json:"version"`
	MakePortSymbolic *makePortSymbolicAction `json:"make_port_symbolic,omitempty"`
}

func (s *svcActions) actions() (actions []action) {
	if s.MakePortSymbolic != nil {
		actions = append(actions, s.MakePortSymbolic)
	}
	return actions
}

func (s *svcActions) do(svc kates.Object) (err error) {
	return doActions(s, svc)
}

func (s *svcActions) explainDo(svc kates.Object, out io.Writer) {
	explainActions(s, svc, action.explainDo, out)
}

func (s *svcActions) explainUndo(svc kates.Object, out io.Writer) {
	explainActions(s, svc, action.explainUndo, out)
}

func (s *svcActions) isDone(svc kates.Object) bool {
	return isDoneActions(s, svc)
}

func (s *svcActions) undo(svc kates.Object) (err error) {
	return undoActions(s, svc)
}

func (s *svcActions) objectType() string {
	return "service"
}

func (s *svcActions) String() string {
	return mustMarshal(s)
}

func (s *svcActions) version() string {
	return s.Version
}

// addTrafficAgentAction is an action that adds a traffic-agent to the set of
// containers in a pod template spec.
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

func (ata *addTrafficAgentAction) appContainer(dep *kates.Deployment) *kates.Container {
	cns := dep.Spec.Template.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == ata.containerName {
			return cn
		}
	}
	return nil
}

func (ata *addTrafficAgentAction) do(obj kates.Object) error {
	dep := obj.(*kates.Deployment)
	appContainer := ata.appContainer(dep)
	if appContainer == nil {
		return fmt.Errorf("unable to find app container %s in deployment %s", ata.containerName, dep.GetName())
	}

	tplSpec := &dep.Spec.Template.Spec
	tplSpec.Containers = append(tplSpec.Containers, corev1.Container{
		Name:  agentContainerName,
		Image: ata.ImageName,
		Args:  []string{"agent"},
		Ports: []corev1.ContainerPort{{
			Name:          ata.ContainerPortName,
			Protocol:      ata.ContainerPortProto,
			ContainerPort: 9900,
		}},
		Env:          ata.agentEnvironment(dep.GetName(), appContainer),
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

const envPrefix = "TEL_APP_"

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
			Name: "AGENT_POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
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
	if mounts == nil {
		return nil
	}
	agentMounts := make([]corev1.VolumeMount, len(mounts))
	for i, mount := range mounts {
		mount.MountPath = filepath.Join(telAppMountPoint, mount.MountPath)
		agentMounts[i] = mount
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

func (ata *addTrafficAgentAction) explainDo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "add traffic-agent container with image %s", ata.ImageName)
}

func (ata *addTrafficAgentAction) explainUndo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "remove traffic-agent container with image %s", ata.ImageName)
}

func (ata *addTrafficAgentAction) isDone(dep kates.Object) bool {
	cns := dep.(*kates.Deployment).Spec.Template.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agentContainerName {
			return true
		}
	}
	return false
}

func (ata *addTrafficAgentAction) undo(dep kates.Object) error {
	tplSpec := &dep.(*kates.Deployment).Spec.Template.Spec
	cns := tplSpec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name != agentContainerName {
			continue
		}

		// remove and keep order
		copy(cns[i:], cns[i+1:])
		last := len(cns) - 1
		cns[last] = kates.Container{}
		tplSpec.Containers = cns[:last]
		break
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

func (hcp *hideContainerPortAction) getPort(dep kates.Object, name string) (*kates.Container, *corev1.ContainerPort, error) {
	cns := dep.(*kates.Deployment).Spec.Template.Spec.Containers
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
	return nil, nil, fmt.Errorf("unable to locate port %s in container %s in deployment %s", hcp.PortName, hcp.ContainerName, dep.GetName())
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

func (hcp *hideContainerPortAction) do(dep kates.Object) error {
	// New name must be max 15 characters long
	cn, p, err := hcp.getPort(dep, hcp.PortName)
	if err != nil {
		return err
	}
	hcp.HiddenName = "tel2mv-" + p.Name
	swapPortName(cn, p, hcp.PortName, hcp.HiddenName)
	return nil
}

func (hcp *hideContainerPortAction) explainDo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "hide port %q in container %s from service by renaming it to %q",
		hcp.PortName, hcp.ContainerName, hcp.HiddenName)
}

func (hcp *hideContainerPortAction) explainUndo(_ kates.Object, out io.Writer) {
	fmt.Fprintf(out, "reveal hidden port %q in container %s by restoring its origina name %q",
		hcp.HiddenName, hcp.ContainerName, hcp.PortName)
}

func (hcp *hideContainerPortAction) isDone(dep kates.Object) bool {
	_, _, err := hcp.getPort(dep, hcp.HiddenName)
	return err == nil
}

func (hcp *hideContainerPortAction) undo(dep kates.Object) error {
	cn, p, err := hcp.getPort(dep, hcp.HiddenName)
	if err != nil {
		return err
	}
	swapPortName(cn, p, hcp.HiddenName, hcp.PortName)
	return nil
}

type deploymentActions struct {
	Version                   string `json:"version"`
	ReferencedService         string
	ReferencedServicePortName string                   `json:"referenced_service_port_name,omitempty"`
	HideContainerPort         *hideContainerPortAction `json:"hide_container_port,omitempty"`
	AddTrafficAgent           *addTrafficAgentAction   `json:"add_traffic_agent,omitempty"`
}

func (d *deploymentActions) actions() (actions []action) {
	if d.HideContainerPort != nil {
		actions = append(actions, d.HideContainerPort)
	}
	if d.AddTrafficAgent != nil {
		actions = append(actions, d.AddTrafficAgent)
	}
	return actions
}

func (d *deploymentActions) explainDo(dep kates.Object, out io.Writer) {
	explainActions(d, dep, action.explainDo, out)
}

func (d *deploymentActions) do(dep kates.Object) (err error) {
	return doActions(d, dep)
}

func (d *deploymentActions) explainUndo(dep kates.Object, out io.Writer) {
	explainActions(d, dep, action.explainUndo, out)
}

func (d *deploymentActions) isDone(dep kates.Object) bool {
	return isDoneActions(d, dep)
}

func (d *deploymentActions) undo(dep kates.Object) (err error) {
	return undoActions(d, dep)
}

func (d *deploymentActions) objectType() string {
	return "deployment"
}

func (d *deploymentActions) String() string {
	return mustMarshal(d)
}

func (d *deploymentActions) version() string {
	return d.Version
}
