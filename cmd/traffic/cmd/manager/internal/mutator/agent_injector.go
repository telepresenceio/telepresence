package mutator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator/agentconfig"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/install/agent"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

var podResource = meta.GroupVersionResource{Version: "v1", Group: "", Resource: "pods"}

type agentInjector struct {
	agentConfigs agentconfig.Map
	terminating  int64
}

func getPod(req *admission.AdmissionRequest) (*core.Pod, error) {
	if req.Resource != podResource {
		return nil, fmt.Errorf("expect resource to be %s, got %s", podResource, req.Resource)
	}

	// Parse the Pod object.
	raw := req.Object.Raw
	pod := core.Pod{}
	if _, _, err := universalDeserializer.Decode(raw, nil, &pod); err != nil {
		return nil, fmt.Errorf("could not deserialize pod object: %v", err)
	}

	podNamespace := pod.Namespace
	if podNamespace == "" {
		// It is very probable the pod was not yet assigned a namespace,
		// in which case we should use the AdmissionRequest namespace.
		pod.Namespace = req.Namespace
	}
	podName := pod.Name
	if podName == "" {
		// It is very probable the pod was not yet assigned a name,
		// in which case we should use the metadata generated name.
		pod.Name = pod.ObjectMeta.GenerateName
	}

	// Validate traffic-agent injection preconditions.
	if pod.Name == "" || pod.Namespace == "" {
		return nil, fmt.Errorf(`unable to extract pod name and/or namespace (got "%s.%s")`, pod.Name, pod.Namespace)
	}
	return &pod, nil
}

func (a *agentInjector) inject(ctx context.Context, req *admission.AdmissionRequest) (patchOps, error) {
	if atomic.LoadInt64(&a.terminating) > 0 || req.Operation == admission.Delete {
		return nil, nil
	}

	pod, err := getPod(req)
	if err != nil {
		return nil, err
	}

	var config *agent.Config
	ia := pod.Annotations[agent.InjectAnnotation]
	switch ia {
	case "false", "disabled":
		dlog.Debugf(ctx, `The %s.%s pod is explicitly disabled using a %q annotation; skipping`, pod.Name, pod.Namespace, agent.InjectAnnotation)
		return nil, nil
	case "", "enabled":
		config, err = a.findConfigMapValue(ctx, pod, nil)
		if err != nil {
			if strings.Contains(err.Error(), "unsupported workload kind") {
				// This isn't something that we want to touch
				err = nil
			}
			return nil, err
		}
		if config == nil && ia == "" {
			dlog.Debugf(ctx, `The %s.%s pod has not enabled %s container injection through %q configmap or %q annotation; skipping`,
				pod.Name, pod.Namespace, agent.ContainerName, agent.ConfigMap, agent.InjectAnnotation)
			return nil, nil
		}
		if config, err = agentconfig.GenerateForPod(ctx, pod); err != nil {
			return nil, err
		}
		if err = a.agentConfigs.Store(ctx, config, true); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid value %q for annotation %s", ia, agent.InjectAnnotation)
	}

	// Create patch operations to add the traffic-agent sidecar
	dlog.Infof(ctx, "Injecting %s into pod %s.%s", agent.ContainerName, pod.Name, pod.Namespace)

	var patches patchOps
	patches = addInitContainer(ctx, pod, config, patches)
	patches = addAgentContainer(ctx, pod, config, patches)
	patches = addAgentVolume(pod, config, patches)
	patches = hidePorts(pod, config, patches)
	patches = addPodAnnotations(ctx, pod, patches)

	env := managerutil.GetEnv(ctx)
	if env.APIPort != 0 {
		tpEnv := make(map[string]string)
		tpEnv["TELEPRESENCE_API_PORT"] = strconv.Itoa(int(env.APIPort))
		patches = addTPEnv(pod, config, tpEnv, patches)
	}
	dlog.Infof(ctx, "Patches = %s", patches)
	return patches, nil
}

// uninstall ensures that no more webhook injections is made and that all the workloads of currently injected
// pods are rolled out.
func (a *agentInjector) uninstall(ctx context.Context) {
	atomic.StoreInt64(&a.terminating, 1)
	a.agentConfigs.DeleteMapsAndRolloutAll(ctx)
}

func needInitContainer(config *agent.Config) bool {
	for _, cc := range config.Containers {
		for _, ic := range cc.Intercepts {
			if ic.Headless || ic.ContainerPortName == "" {
				return true
			}
		}
	}
	return false
}

func addInitContainer(ctx context.Context, pod *core.Pod, config *agent.Config, patches patchOps) patchOps {
	if !needInitContainer(config) {
		for i, oc := range pod.Spec.InitContainers {
			if agent.InitContainerName == oc.Name {
				return append(patches, patchOperation{
					Op:   "remove",
					Path: fmt.Sprintf("/spec/initContainers/%d", i),
				})
			}
		}
		return patches
	}

	env := managerutil.GetEnv(ctx)
	ic := core.Container{
		Name:  agent.InitContainerName,
		Image: env.AgentRegistry + "/" + env.AgentImage,
		Args:  []string{"agent-init"},
		VolumeMounts: []core.VolumeMount{{
			Name:      agent.ConfigVolumeName,
			MountPath: agent.ConfigMountPoint,
		}},
		SecurityContext: &core.SecurityContext{
			Capabilities: &core.Capabilities{
				Add: []core.Capability{"NET_ADMIN"},
			},
		},
	}

	if pod.Spec.InitContainers == nil {
		return append(patches, patchOperation{
			Op:    "replace",
			Path:  "/spec/initContainers",
			Value: []core.Container{ic},
		})
	}

	for i, oc := range pod.Spec.InitContainers {
		if ic.Name == oc.Name {
			if containerEqual(&ic, &oc) {
				return patches
			}
			return append(patches, patchOperation{
				Op:    "replace",
				Path:  fmt.Sprintf("/spec/initContainers/%d", i),
				Value: ic,
			})
		}
	}

	return append(patches, patchOperation{
		Op:    "add",
		Path:  "/spec/initContainers/-",
		Value: ic,
	})
}

func addAgentVolume(pod *core.Pod, ag *agent.Config, patches patchOps) patchOps {
	for _, vol := range pod.Spec.Volumes {
		if vol.Name == agent.AnnotationVolumeName {
			return patches
		}
	}
	return append(patches,
		patchOperation{
			Op:   "add",
			Path: "/spec/volumes/-",
			Value: core.Volume{
				Name: agent.AnnotationVolumeName,
				VolumeSource: core.VolumeSource{
					DownwardAPI: &core.DownwardAPIVolumeSource{
						Items: []core.DownwardAPIVolumeFile{
							{
								FieldRef: &core.ObjectFieldSelector{
									APIVersion: "v1",
									FieldPath:  "metadata.annotations",
								},
								Path: "annotations",
							},
						},
					},
				},
			},
		},
		patchOperation{
			Op:   "add",
			Path: "/spec/volumes/-",
			Value: core.Volume{
				Name: agent.ConfigVolumeName,
				VolumeSource: core.VolumeSource{
					ConfigMap: &core.ConfigMapVolumeSource{
						LocalObjectReference: core.LocalObjectReference{Name: agent.ConfigMap},
						Items: []core.KeyToPath{{
							Key:  ag.AgentName,
							Path: agent.ConfigFile,
						}},
					},
				},
			},
		},
	)
}

// compareProbes compares two Probes but will only consider their Handler.Exec.Command in the comparison
func compareProbes(a, b *core.Probe) bool {
	if a == nil || b == nil {
		return a == b
	}
	ae := a.ProbeHandler.Exec
	be := b.ProbeHandler.Exec
	if ae == nil || be == nil {
		return ae == be
	}
	eq := cmp.Equal(ae.Command, be.Command)
	return eq
}

// compareVolumeMounts compares two VolumeMount slices but will not include volume mounts using "kube-api-access-" prefix
func compareVolumeMounts(a, b []core.VolumeMount) bool {
	stripKubeAPI := func(vs []core.VolumeMount) []core.VolumeMount {
		ss := make([]core.VolumeMount, 0, len(vs))
		for _, v := range vs {
			if !strings.HasPrefix(v.Name, "kube-api-access-") {
				ss = append(ss, v)
			}
		}
		return ss
	}
	eq := cmp.Equal(stripKubeAPI(a), stripKubeAPI(b))
	return eq
}

func containerEqual(a, b *core.Container) bool {
	// skips contain defaults assigned by Kubernetes that are not zero values
	return cmp.Equal(a, b,
		cmp.Comparer(compareProbes),
		cmp.Comparer(compareVolumeMounts),
		cmpopts.IgnoreFields(core.Container{}, "ImagePullPolicy", "TerminationMessagePath", "TerminationMessagePolicy"))
}

// addAgentContainer creates a patch operation to add the traffic-agent container
func addAgentContainer(
	ctx context.Context,
	pod *core.Pod,
	config *agent.Config,
	patches patchOps,
) patchOps {
	acn := agentContainer(pod, config)
	if acn == nil {
		return patches
	}

	refPodName := pod.Name + "." + pod.Namespace
	for i := range pod.Spec.Containers {
		pcn := &pod.Spec.Containers[i]
		if pcn.Name == agent.ContainerName {
			if containerEqual(pcn, acn) {
				dlog.Infof(ctx, "Pod %s already has container %s and it isn't modified", refPodName, agentconfig.ContainerName)
				return patches
			}
			dlog.Debugf(ctx, "Pod %s already has container %s but it is modified", refPodName, agent.ContainerName)
			return append(patches, patchOperation{
				Op:    "replace",
				Path:  "/spec/containers/" + strconv.Itoa(i),
				Value: acn})
		}
	}

	return append(patches, patchOperation{
		Op:    "add",
		Path:  "/spec/containers/-",
		Value: acn})
}

// addTPEnv adds telepresence specific environment variables to all interceptable app containers
func addTPEnv(pod *core.Pod, config *agent.Config, env map[string]string, patches patchOps) patchOps {
	eachContainer(pod, config, func(app *core.Container, cc *agent.Container) {
		patches = addContainerTPEnv(pod, app, env, patches)
	})
	return patches
}

// addContainerTPEnv adds telepresence specific environment variables to the app container
func addContainerTPEnv(pod *core.Pod, cn *core.Container, env map[string]string, patches patchOps) patchOps {
	if l := len(cn.Env); l > 0 {
		for _, e := range cn.Env {
			if e.ValueFrom == nil && env[e.Name] == e.Value {
				delete(env, e.Name)
			}
		}
	}
	if len(env) == 0 {
		return patches
	}
	cns := pod.Spec.Containers
	var containerPath string
	for i := range cns {
		if &cns[i] == cn {
			containerPath = fmt.Sprintf("/spec/containers/%d", i)
			break
		}
	}
	keys := make([]string, len(env))
	i := 0
	for k := range env {
		keys[i] = k
		i++
	}
	sort.Strings(keys)
	if cn.Env == nil {
		patches = append(patches, patchOperation{
			Op:    "replace",
			Path:  fmt.Sprintf("%s/%s", containerPath, "env"),
			Value: []core.EnvVar{},
		})
	}
	for _, k := range keys {
		patches = append(patches, patchOperation{
			Op:   "add",
			Path: fmt.Sprintf("%s/%s", containerPath, "env/-"),
			Value: core.EnvVar{
				Name:      k,
				Value:     env[k],
				ValueFrom: nil,
			},
		})
	}
	return patches
}

// hidePorts  will replace the symbolic name of a container port with a generated name. It will perform
// the same replacement on all references to that port from the probes of the container
func hidePorts(pod *core.Pod, config *agent.Config, patches patchOps) patchOps {
	eachContainer(pod, config, func(app *core.Container, cc *agent.Container) {
		for _, ic := range cc.Intercepts {
			if ic.Headless || ic.ContainerPortName == "" {
				// Rely on iptables mapping instead of port renames
				continue
			}
			patches = hideContainerPorts(pod, app, ic.ContainerPortName, patches)
		}
	})
	return patches
}

// hideContainerPorts  will replace the symbolic name of a container port with a generated name. It will perform
// the same replacement on all references to that port from the probes of the container
func hideContainerPorts(pod *core.Pod, app *core.Container, portName string, patches patchOps) patchOps {
	cns := pod.Spec.Containers
	var containerPath string
	for i := range cns {
		if &cns[i] == app {
			containerPath = fmt.Sprintf("/spec/containers/%d", i)
			break
		}
	}

	hiddenPortName := install.HiddenPortName(portName, 0)
	hidePort := func(path string) {
		patches = append(patches, patchOperation{
			Op:    "replace",
			Path:  fmt.Sprintf("%s/%s", containerPath, path),
			Value: hiddenPortName,
		})
	}

	for i, p := range app.Ports {
		if p.Name == portName {
			hidePort(fmt.Sprintf("ports/%d/name", i))
			break
		}
	}

	probes := []*core.Probe{app.LivenessProbe, app.ReadinessProbe, app.StartupProbe}
	probeNames := []string{"livenessProbe/", "readinessProbe/", "startupProbe/"}

	for i, probe := range probes {
		if probe == nil {
			continue
		}
		if h := probe.HTTPGet; h != nil && h.Port.StrVal == portName {
			hidePort(probeNames[i] + "httpGet/port")
		}
		if t := probe.TCPSocket; t != nil && t.Port.StrVal == portName {
			hidePort(probeNames[i] + "tcpSocket/port")
		}
	}
	return patches
}

func addPodAnnotations(_ context.Context, pod *core.Pod, patches patchOps) patchOps {
	op := "replace"
	changed := false
	am := pod.Annotations
	if am == nil {
		op = "add"
		am = make(map[string]string)
	} else {
		cm := make(map[string]string, len(am))
		for k, v := range am {
			cm[k] = v
		}
		am = cm
	}

	if _, ok := pod.Annotations[agent.InjectAnnotation]; !ok {
		changed = true
		am[agent.InjectAnnotation] = "enabled"
	}

	if changed {
		patches = append(patches, patchOperation{
			Op:    op,
			Path:  "/metadata/annotations",
			Value: am,
		})
	}
	return patches
}

func (a *agentInjector) findConfigMapValue(ctx context.Context, pod *core.Pod, wl k8sapi.Workload) (*agent.Config, error) {
	if a.agentConfigs == nil {
		return nil, nil
	}
	var refs []meta.OwnerReference
	if wl != nil {
		ag := agent.Config{}
		ok, err := a.agentConfigs.GetInto(agentconfig.AgentName(wl), pod.GetNamespace(), &ag)
		if err != nil {
			return nil, err
		}
		if ok && (ag.WorkloadKind == "" || ag.WorkloadKind == wl.GetKind()) {
			return &ag, nil
		}
		refs = wl.GetOwnerReferences()
	} else {
		refs = pod.GetOwnerReferences()
	}
	for i := range refs {
		if or := &refs[i]; or.Controller != nil && *or.Controller {
			wl, err := k8sapi.GetWorkload(ctx, or.Name, pod.GetNamespace(), or.Kind)
			if err != nil {
				return nil, err
			}
			return a.findConfigMapValue(ctx, pod, wl)
		}
	}
	return nil, nil
}

// agentContainer will return a configured traffic-agent
func agentContainer(
	pod *core.Pod,
	config *agent.Config,
) *core.Container {
	ports := make([]core.ContainerPort, 0, 5)
	for _, cc := range config.Containers {
		for _, ic := range cc.Intercepts {
			ports = append(ports, core.ContainerPort{
				Name:          ic.ContainerPortName,
				ContainerPort: int32(ic.AgentPort),
				Protocol:      core.Protocol(ic.Protocol),
			})
		}
	}
	if len(ports) == 0 {
		return nil
	}

	evs := make([]core.EnvVar, 0, len(config.Containers)*5)
	efs := make([]core.EnvFromSource, 0, len(config.Containers)*3)
	eachContainer(pod, config, func(app *core.Container, cc *agent.Container) {
		evs = appendAppContainerEnv(app, cc, evs)
		efs = appendAppContainerEnvFrom(app, cc, efs)
	})
	evs = append(evs,
		core.EnvVar{
			Name: agent.EnvPrefixAgent + "POD_IP",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "status.podIP",
				},
			},
		})

	mounts := make([]core.VolumeMount, 0, len(config.Containers)*3)
	eachContainer(pod, config, func(app *core.Container, cc *agent.Container) {
		mounts = appendAppContainerVolumeMounts(app, cc, mounts)
	})

	mounts = append(mounts, core.VolumeMount{
		Name:      agent.AnnotationVolumeName,
		MountPath: agent.AnnotationMountPoint,
	})
	mounts = append(mounts, core.VolumeMount{
		Name:      agent.ConfigVolumeName,
		MountPath: agent.ConfigMountPoint,
	})

	if len(efs) == 0 {
		efs = nil
	}
	return &core.Container{
		Name:         agent.ContainerName,
		Image:        config.AgentImage,
		Args:         []string{"agent"},
		Ports:        ports,
		Env:          evs,
		EnvFrom:      efs,
		VolumeMounts: mounts,
		ReadinessProbe: &core.Probe{
			ProbeHandler: core.ProbeHandler{
				Exec: &core.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	}
}

// eachContainer will find each container in the given config and match it against a container
// in the pod using its name. The given function is called once for each match.
func eachContainer(pod *core.Pod, config *agent.Config, f func(*core.Container, *agent.Container)) {
	cns := pod.Spec.Containers
	for _, cc := range config.Containers {
		for i := range pod.Spec.Containers {
			if app := &cns[i]; app.Name == cc.Name {
				f(app, cc)
				break
			}
		}
	}
}

func appendAppContainerVolumeMounts(app *core.Container, cc *agent.Container, mounts []core.VolumeMount) []core.VolumeMount {
	for _, m := range app.VolumeMounts {
		if strings.HasPrefix(m.MountPath, "/var/run/secrets/") {
			// Trust that those are injected into the agent container
			continue
		}
		m.MountPath = cc.MountPoint + "/" + strings.TrimPrefix(m.MountPath, "/")
		mounts = append(mounts, m)
	}
	return mounts
}

func appendAppContainerEnv(app *core.Container, cc *agent.Container, es []core.EnvVar) []core.EnvVar {
	for _, e := range app.Env {
		e.Name = agent.EnvPrefixApp + cc.EnvPrefix + e.Name
		es = append(es, e)
	}
	return es
}

func appendAppContainerEnvFrom(app *core.Container, cc *agent.Container, es []core.EnvFromSource) []core.EnvFromSource {
	for _, e := range app.EnvFrom {
		e.Prefix = agent.EnvPrefixApp + cc.EnvPrefix + e.Prefix
		es = append(es, e)
	}
	return es
}
