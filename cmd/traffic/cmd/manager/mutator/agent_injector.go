package mutator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

var podResource = meta.GroupVersionResource{Version: "v1", Group: "", Resource: "pods"} //nolint:gochecknoglobals // constant

type AgentInjector interface {
	Inject(ctx context.Context, req *admission.AdmissionRequest) (p PatchOps, err error)
	Uninstall(ctx context.Context)
}

// NewAgentInjector creates a new agentInjector.
func NewAgentInjector(_ context.Context, agentConfigs Map) AgentInjector {
	ai := &agentInjector{
		agentConfigs: agentConfigs,
	}
	return ai
}

var NewAgentInjectorFunc = NewAgentInjector //nolint:gochecknoglobals // extension point

type agentInjector struct {
	sync.Mutex
	agentConfigs Map
	terminating  int64
}

func getPod(req *admission.AdmissionRequest, isDelete bool) (*core.Pod, error) {
	if req.Resource != podResource {
		return nil, fmt.Errorf("expect resource to be %s, got %s", podResource, req.Resource)
	}

	// Parse the Pod object.
	var raw []byte
	if isDelete {
		raw = req.OldObject.Raw
	} else {
		raw = req.Object.Raw
	}
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
		pod.Name = pod.GenerateName
	}

	// Validate traffic-agent injection preconditions.
	if pod.Name == "" || pod.Namespace == "" {
		return nil, fmt.Errorf(`unable to extract pod name and/or namespace (got "%s.%s")`, pod.Name, pod.Namespace)
	}
	return &pod, nil
}

func (a *agentInjector) Inject(ctx context.Context, req *admission.AdmissionRequest) (p PatchOps, err error) {
	isDelete := req.Operation == admission.Delete
	if atomic.LoadInt64(&a.terminating) > 0 {
		clog.Debugf(ctx, "Skipping webhook for %s.%s because the agent-injector is terminating", req.Name, req.Namespace)
		return nil, nil
	}

	pod, err := getPod(req, isDelete)
	if err != nil {
		return nil, err
	}

	if isDelete {
		a.agentConfigs.Inactivate(pod.UID)
		return nil, nil
	}

	clog.Debugf(ctx, "Handling admission request %s %s.%s", req.Operation, pod.Name, pod.Namespace)
	env := managerutil.GetEnv(ctx)

	ia := annotation.GetAnnotation(ctx, pod.Annotations, annotation.InjectTrafficAgent, annotation.LegacyInjectTrafficAgent)

	var wl k8sapi.Workload
	var sc *agentconfig.Sidecar
	switch ia {
	case "false", "disabled":
		clog.Debugf(ctx, `The %s.%s pod is explicitly disabled using a %q annotation; skipping`, pod.Name, pod.Namespace, annotation.InjectTrafficAgent)
		return nil, nil
	case "":
		if env.AgentInjectPolicy != agentconfig.OnDemand {
			clog.Debugf(ctx, `The %s.%s pod has not enabled %s container injection through %q annotation; skipping`,
				pod.Name, pod.Namespace, agentconfig.ContainerName, annotation.InjectTrafficAgent)
			return nil, nil
		}
		fallthrough
	case "enabled":
		img := managerutil.GetAgentImage(ctx)
		if img == "" {
			clog.Debug(ctx, "Skipping webhook injection because the traffic-manager is unable to determine what image to use for injected traffic-agents.")
			return nil, nil
		}

		wl, err = agentmap.FindOwnerWorkload(ctx, k8sapi.Pod(pod), env.EnabledWorkloadKinds)
		if err != nil {
			uwkError := k8sapi.UnsupportedWorkloadKindError("")
			switch {
			case k8sErrors.IsNotFound(err):
				clog.Tracef(ctx, "No workload owner found for pod %s.%s", pod.Name, pod.Namespace)
			case errors.As(err, &uwkError):
				clog.Debugf(ctx, "Workload owner with %s found for pod %s.%s", uwkError.Error(), pod.Name, pod.Namespace)
			default:
				clog.Debugf(ctx, "No workload owner found for pod %s.%s: %v", pod.Name, pod.Namespace, err)
			}
			// Not an error. It just means that the pod is not eligible for intercepts.
			return nil, nil
		}
		if ia == "enabled" {
			sc, err = a.agentConfigs.GetOrGenerate(ctx, wl)
			if err != nil {
				return nil, fmt.Errorf("unable to get or generate agent config for workload %s.%s: %w", wl.GetNamespace(), wl.GetName(), err)
			}
		} else {
			sc = a.agentConfigs.Get(wl.GetName(), wl.GetNamespace())
			if sc == nil {
				clog.Tracef(ctx, "Skipping %s (no agent config)", wl)
				return nil, nil
			}
		}
		if sc.Manual {
			clog.Tracef(ctx, "Skipping webhook where agent is manually injected %s", wl.GetNamespace())
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("invalid value %q for annotation %s", ia, annotation.InjectTrafficAgent)
	}

	podTpl := &core.PodTemplateSpec{ObjectMeta: pod.ObjectMeta}
	if env.AgentInjectorMutationAware {
		podTpl.Spec = pod.Spec
	} else {
		podTpl.Spec = wl.GetPodTemplate().Spec
	}
	return createPatch(ctx, sc, pod, podTpl)
}

func createPatch(ctx context.Context, config *agentconfig.Sidecar, pod *core.Pod, wlTpl *core.PodTemplateSpec) (patches PatchOps, err error) {
	var anns map[string]string
	patches = addInitContainer(ctx, pod, config, patches)
	patches, anns, err = addAgentContainer(ctx, pod, wlTpl, config, patches)
	if err != nil {
		return nil, err
	}
	patches = addPullSecrets(pod, config, patches)
	patches, err = addAgentVolumes(config.AgentName, pod, patches)
	if err != nil {
		return nil, err
	}
	patches = hidePorts(pod, config, patches)
	anns[annotation.InjectTrafficAgent] = "enabled"
	patches = addPodAnnotations(pod, anns, patches)
	patches = addPodLabels(ctx, pod, config, patches)
	patches = maybeRemoveAppContainer(pod, config, patches)

	if config.APIPort != 0 {
		tpEnv := make(map[string]string)
		tpEnv[agentconfig.EnvAPIHost] = "localhost"
		tpEnv[agentconfig.EnvAPIPort] = strconv.Itoa(int(config.APIPort))
		patches = addTPEnv(pod, config, tpEnv, patches)
	}

	// Create patch operations to add the traffic-agent sidecar
	if len(patches) > 0 {
		clog.Debugf(ctx, "Injecting %d patches into pod %s.%s", len(patches), pod.Name, pod.Namespace)
		if clog.Enabled(ctx, clog.LevelTrace) {
			cns := strings.Builder{}
			for i, cn := range pod.Spec.Containers {
				cns.WriteString(fmt.Sprintf("%d %s\n", i, cn.Name))
			}
			clog.Tracef(ctx, "Containers \n%s", cns.String())
			if pj, err := json.Marshal(patches, jsontext.WithIndent("  ")); err == nil {
				clog.Tracef(ctx, "\n%s", string(pj))
			}
		}
	} else {
		clog.Debugf(ctx, "Pod %s.%s was left untouched", pod.Name, pod.Namespace)
	}
	return patches, nil
}

// Uninstall ensures that no more webhook injections are made and that all the workloads of currently injected
// pods are rolled out.
func (a *agentInjector) Uninstall(ctx context.Context) {
	atomic.StoreInt64(&a.terminating, 1)
	a.agentConfigs.DeleteMapsAndRolloutAll(ctx)
}

func needInitContainer(ctx context.Context, config *agentconfig.Sidecar) bool {
	if !managerutil.GetEnv(ctx).AgentInitContainerEnabled {
		clog.Info(ctx, "Injection of initContainer is disabled in the config. It is enabled by default and can be modified by setting agent.initContainer.enabled in values.yaml")
		return false
	}
	for _, cc := range config.Containers {
		if cc.Replace == agentconfig.ReplacePolicyIntercept {
			for _, ic := range cc.Intercepts {
				if ic.Headless || ic.TargetPortNumeric {
					return true
				}
			}
		}
	}
	return false
}

func maybeRemoveAppContainer(pod *core.Pod, config *agentconfig.Sidecar, patches PatchOps) PatchOps {
	// Extremely important to remove in reverse order, or one may affect the index of the next removal.
	cns := pod.Spec.Containers
	for i := len(cns) - 1; i >= 0; i-- {
		for _, cc := range config.Containers {
			if cc.Name == cns[i].Name && cc.Replace == agentconfig.ReplacePolicyContainer {
				patches = append(patches, PatchOperation{
					Op:   "remove",
					Path: fmt.Sprintf("/spec/containers/%d", i),
				})
			}
		}
	}
	return patches
}

func addInitContainer(ctx context.Context, pod *core.Pod, config *agentconfig.Sidecar, patches PatchOps) PatchOps {
	if !needInitContainer(ctx, config) {
		for i, oc := range pod.Spec.InitContainers {
			if agentconfig.InitContainerName == oc.Name {
				return append(patches, PatchOperation{
					Op:   "remove",
					Path: fmt.Sprintf("/spec/initContainers/%d", i),
				})
			}
		}
		return patches
	}

	pis := pod.Spec.InitContainers
	ic := agentconfig.InitContainer(config)
	if len(pis) == 0 {
		return append(patches, PatchOperation{
			Op:    "replace",
			Path:  "/spec/initContainers",
			Value: []core.Container{*ic},
		})
	}

	for i := range pis {
		oc := &pis[i]
		if ic.Name == oc.Name {
			if ic.Image == oc.Image &&
				slices.Equal(ic.Args, oc.Args) &&
				compareVolumeMounts(ic.VolumeMounts, oc.VolumeMounts) &&
				compareCapabilities(ic.SecurityContext, oc.SecurityContext) {
				return patches
			}
			return append(patches, PatchOperation{
				Op:    "replace",
				Path:  fmt.Sprintf("/spec/initContainers/%d", i),
				Value: ic,
			})
		}
	}

	return append(patches, PatchOperation{
		Op:    "add",
		Path:  "/spec/initContainers/-",
		Value: ic,
	})
}

func addAgentVolumes(agentName string, pod *core.Pod, patches PatchOps) (PatchOps, error) {
	for _, vol := range pod.Spec.Volumes {
		if vol.Name == agentconfig.ExportsVolumeName {
			return patches, nil
		}
	}
	avs, err := agentconfig.AgentVolumes(agentName, pod)
	if err != nil {
		return nil, fmt.Errorf("unable to create agent volumes: %w", err)
	}
	if len(avs) == 0 {
		return patches, nil
	}

	// Ensure that /spec/volumes exists in the pod. It won't be present when the pod doesn't have
	// any volumes and automountServiceAccountToken == false
	if pod.Spec.Volumes == nil {
		patches = append(patches,
			PatchOperation{
				Op:    "replace",
				Path:  "/spec/volumes",
				Value: avs,
			})
	} else {
		for _, av := range avs {
			patches = append(patches,
				PatchOperation{
					Op:    "add",
					Path:  "/spec/volumes/-",
					Value: av,
				})
		}
	}
	return patches, nil
}

// compareProbes compares two Probes but will only consider their Handler.Exec.Command in the comparison.
func compareProbes(a, b *core.Probe) bool {
	if a == nil || b == nil {
		return a == b
	}
	ae := a.Exec
	be := b.Exec
	if ae == nil || be == nil {
		return ae == be
	}
	eq := cmp.Equal(ae.Command, be.Command)
	return eq
}

func compareCapabilities(a *core.SecurityContext, b *core.SecurityContext) bool {
	ac := a.Capabilities
	bc := b.Capabilities
	if ac == bc {
		return true
	}
	if ac == nil || bc == nil {
		return false
	}
	compareCaps := func(acs []core.Capability, bcs []core.Capability) bool {
		if len(acs) != len(bcs) {
			return false
		}
		for i := range acs {
			if acs[i] != bcs[i] {
				return false
			}
		}
		return true
	}
	return compareCaps(ac.Add, bc.Add) && compareCaps(ac.Drop, bc.Drop)
}

// compareVolumeMounts compares two VolumeMount slices but will not include volume mounts using "kube-api-access-" prefix.
func compareVolumeMounts(a, b []core.VolumeMount) bool {
	stripKubeAPI := func(vs []core.VolumeMount) []core.VolumeMount {
		ss := make([]core.VolumeMount, 0, len(vs))
		for _, v := range vs {
			if !(strings.HasPrefix(v.Name, "kube-api-access-") || strings.HasPrefix(v.MountPath, "/var/run/secrets/kubernetes.io/")) {
				ss = append(ss, v)
			}
		}
		return ss
	}
	eq := cmp.Equal(stripKubeAPI(a), stripKubeAPI(b))
	return eq
}

func containerEqual(ctx context.Context, a, b *core.Container) bool {
	// skips contain defaults assigned by Kubernetes that are not zero values
	options := cmp.Options{
		cmp.Comparer(compareProbes),
		cmp.Comparer(compareVolumeMounts),
		cmpopts.IgnoreFields(core.Container{}, "ImagePullPolicy", "Resources", "TerminationMessagePath", "TerminationMessagePolicy"),
	}
	if clog.Enabled(ctx, slog.LevelDebug) {
		diff := cmp.Diff(a, b, options...)
		if diff != "" {
			clog.Debug(ctx, diff)
		}
		return diff == ""
	}
	return cmp.Equal(a, b, options...)
}

// addAgentContainer creates a patch operation to add the traffic-agent container.
func addAgentContainer(
	ctx context.Context,
	pod *core.Pod,
	wlTpl *core.PodTemplateSpec,
	config *agentconfig.Sidecar,
	patches PatchOps,
) (PatchOps, map[string]string, error) {
	ab := agentconfig.ContainerBuilder{
		MountPolicies: managerutil.GetEnv(ctx).AgentMountPolicies,
		Pod:           wlTpl,
		Config:        config,
	}
	acn, replaceAnnotations, err := ab.AgentContainer(ctx)
	if err != nil {
		return patches, nil, err
	}
	if acn == nil {
		return patches, replaceAnnotations, nil
	}

	refPodName := pod.Name + "(" + pod.Status.PodIP + ")"
	for i := range pod.Spec.Containers {
		pcn := &pod.Spec.Containers[i]
		if pcn.Name == agentconfig.ContainerName {
			if containerEqual(ctx, pcn, acn) {
				clog.Debugf(ctx, "Pod %s already has container %s and it isn't modified", refPodName, agentconfig.ContainerName)
				return patches, replaceAnnotations, nil
			}
			clog.Debugf(ctx, "Pod %s already has container %s but it is modified", refPodName, agentconfig.ContainerName)
			return append(patches, PatchOperation{
				Op:    "replace",
				Path:  "/spec/containers/" + strconv.Itoa(i),
				Value: acn,
			}), replaceAnnotations, nil
		}
	}

	return append(patches, PatchOperation{
		Op:    "add",
		Path:  "/spec/containers/-",
		Value: acn,
	}), replaceAnnotations, nil
}

// addAgentContainer creates a patch operation to add the traffic-agent container.
func addPullSecrets(
	pod *core.Pod,
	config *agentconfig.Sidecar,
	patches PatchOps,
) PatchOps {
	if len(config.PullSecrets) == 0 {
		return patches
	}
	if len(pod.Spec.ImagePullSecrets) == 0 {
		return append(patches, PatchOperation{
			Op:    "replace",
			Path:  "/spec/imagePullSecrets",
			Value: config.PullSecrets,
		})
	}
	for _, nps := range config.PullSecrets {
		found := false
		for _, ips := range pod.Spec.ImagePullSecrets {
			if nps.Name == ips.Name {
				found = true
				break
			}
		}
		if !found {
			patches = append(patches, PatchOperation{
				Op:    "add",
				Path:  "/spec/imagePullSecrets/-",
				Value: nps,
			})
		}
	}
	return patches
}

// addTPEnv adds telepresence specific environment variables to all interceptable app containers.
func addTPEnv(pod *core.Pod, config *agentconfig.Sidecar, env map[string]string, patches PatchOps) PatchOps {
	config.EachContainer(pod, func(app *core.Container, cc *agentconfig.Container) {
		if cc.Replace != agentconfig.ReplacePolicyContainer {
			patches = addContainerTPEnv(pod, app, env, patches)
		}
	})
	return patches
}

// addContainerTPEnv adds telepresence specific environment variables to the app container.
func addContainerTPEnv(pod *core.Pod, cn *core.Container, env map[string]string, patches PatchOps) PatchOps {
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
		patches = append(patches, PatchOperation{
			Op:    "replace",
			Path:  fmt.Sprintf("%s/%s", containerPath, "env"),
			Value: []core.EnvVar{},
		})
	}
	for _, k := range keys {
		patches = append(patches, PatchOperation{
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
// the same replacement on all references to that port from the probes of the container.
func hidePorts(pod *core.Pod, config *agentconfig.Sidecar, patches PatchOps) PatchOps {
	config.EachContainer(pod, func(app *core.Container, cc *agentconfig.Container) {
		if cc.Replace == agentconfig.ReplacePolicyIntercept {
			for _, ic := range agentconfig.PortUniqueIntercepts(cc) {
				if ic.Headless || ic.TargetPortNumeric {
					// Rely on iptables mapping instead of port renames
					continue
				}
				patches = hideContainerPorts(pod, app, ic.ContainerPortName, patches)
			}
		}
	})
	return patches
}

// hideContainerPorts  will replace the symbolic name of a container port with a generated name. It will perform
// the same replacement on all references to that port from the probes of the container.
func hideContainerPorts(pod *core.Pod, app *core.Container, portName string, patches PatchOps) PatchOps {
	cns := pod.Spec.Containers
	var containerPath string
	for i := range cns {
		if &cns[i] == app {
			containerPath = fmt.Sprintf("/spec/containers/%d", i)
			break
		}
	}

	hiddenPortName := hiddenPortName(portName, 0)
	hidePort := func(path string) {
		patches = append(patches, PatchOperation{
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

	// A replacing intercept will swap the app-container for one that doesn't have any
	// probes, so the patch must not contain renames for those.
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

func addPodAnnotations(pod *core.Pod, anns map[string]string, patches PatchOps) PatchOps {
	op := "replace"
	changed := false
	am := pod.Annotations
	if am == nil {
		op = "add"
		am = make(map[string]string)
	} else {
		am = maps.Copy(am)
	}

	for k, v := range anns {
		if _, ok := pod.Annotations[k]; !ok {
			changed = true
			am[k] = v
		}
	}

	if changed {
		patches = append(patches, PatchOperation{
			Op:    op,
			Path:  "/metadata/annotations",
			Value: am,
		})
	}
	return patches
}

func addPodLabels(_ context.Context, pod *core.Pod, config *agentconfig.Sidecar, patches PatchOps) PatchOps {
	op := "replace"
	changed := false
	lm := pod.Labels
	if lm == nil {
		op = "add"
		lm = make(map[string]string)
	} else {
		lm = maps.Copy(lm)
	}
	if _, ok := pod.Labels[agentconfig.WorkloadNameLabel]; !ok {
		changed = true
		lm[agentconfig.WorkloadNameLabel] = config.WorkloadName
	}
	if _, ok := pod.Labels[agentconfig.WorkloadKindLabel]; !ok {
		changed = true
		lm[agentconfig.WorkloadKindLabel] = string(config.WorkloadKind)
	}
	if _, ok := pod.Labels[agentconfig.WorkloadEnabledLabel]; !ok {
		changed = true
		lm[agentconfig.WorkloadEnabledLabel] = "true"
	}
	if changed {
		patches = append(patches, PatchOperation{
			Op:    op,
			Path:  "/metadata/labels",
			Value: lm,
		})
	}
	return patches
}

const maxPortNameLen = 15

// hiddenPortName prefixes the given name with "tm-" and truncates it to 15 characters. If
// the ordinal is greater than zero, the last two digits are reserved for the hexadecimal
// representation of that ordinal.
func hiddenPortName(name string, ordinal int) string {
	// New name must be max 15 characters long
	hiddenName := "tm-" + name
	if len(hiddenName) > maxPortNameLen {
		if ordinal > 0 {
			hiddenName = hiddenName[:maxPortNameLen-2] + strconv.FormatInt(int64(ordinal), 16) // we don't expect more than 256 ports
		} else {
			hiddenName = hiddenName[:maxPortNameLen]
		}
	}
	return hiddenName
}
