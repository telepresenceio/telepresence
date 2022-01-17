package mutator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

var podResource = meta.GroupVersionResource{Version: "v1", Group: "", Resource: "pods"}
var findMatchingService = install.FindMatchingService

func agentInjector(ctx context.Context, req *admission.AdmissionRequest) ([]patchOperation, error) {
	// This handler should only get called on Pod objects as per the MutatingWebhookConfiguration in the YAML file.
	// Pod objects are immutable, hence we only care about the CREATE event.
	// Applying patches to Pods instead of Deployments means we don't have side effects on
	// user-managed Deployments and Services. It also means we don't have to manage update flows
	// such as removing or updating the sidecar in Deployments objects... a new Pod just gets created instead!

	// If (for whatever reason) this handler is invoked on an object different than a Pod,
	// issue a log message and let the object request pass through.
	if req.Resource != podResource {
		dlog.Debugf(ctx, "expect resource to be %s, got %s; skipping", podResource, req.Resource)
		return nil, nil
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
		podNamespace = req.Namespace
	}
	podName := pod.Name
	if podName == "" {
		// It is very probable the pod was not yet assigned a name,
		// in which case we should use the metadata generated name.
		podName = pod.ObjectMeta.GenerateName
	}

	// Validate traffic-agent injection preconditions.
	refPodName := fmt.Sprintf("%s.%s", podName, podNamespace)
	if podName == "" || podNamespace == "" {
		dlog.Debugf(ctx, "Unable to extract pod name and/or namespace (got %q); skipping", refPodName)
		return nil, nil
	}

	if pod.Annotations[install.InjectAnnotation] != "enabled" {
		dlog.Debugf(ctx, `The %s pod has not enabled %s container injection through %q annotation; skipping`,
			refPodName, install.AgentContainerName, install.InjectAnnotation)
		return nil, nil
	}

	svcName := pod.Annotations[install.ServiceNameAnnotation]
	svc, err := findMatchingService(ctx, "", svcName, podNamespace, pod.Labels)
	if err != nil {
		dlog.Error(ctx, err)
		return nil, err
	}

	// The ServicePortAnnotation is expected to contain a string that identifies the service port.
	portNameOrNumber := pod.Annotations[install.ServicePortAnnotation]
	servicePort, appContainer, containerPortIndex, err := install.FindMatchingPort(pod.Spec.Containers, portNameOrNumber, svc)
	if err != nil {
		err := fmt.Errorf("unable to find port to intercept; try the %s annotation: %w", install.ServicePortAnnotation, err)
		dlog.Error(ctx, err)
		return nil, err
	}
	if appContainer.Name == install.AgentContainerName {
		dlog.Infof(ctx, "service %s/%s is already pointing at agent container %s; skipping", svc.Namespace, svc.Name, appContainer.Name)
		return nil, nil
	}

	env := managerutil.GetEnv(ctx)
	ports := appContainer.Ports
	for i := range ports {
		if ports[i].ContainerPort == env.AgentPort {
			err := fmt.Errorf("the %s pod container %s is exposing the same port (%d) as the %s sidecar", refPodName, appContainer.Name, env.AgentPort, install.AgentContainerName)
			dlog.Info(ctx, err)
			return nil, err
		}
	}

	var appPort core.ContainerPort
	switch {
	case containerPortIndex >= 0:
		appPort = appContainer.Ports[containerPortIndex]
	case servicePort.TargetPort.Type == intstr.Int:
		appPort = core.ContainerPort{
			Protocol:      servicePort.Protocol,
			ContainerPort: servicePort.TargetPort.IntVal,
		}
	default:
		// This really shouldn't have happened: the target port is a string, but we weren't able to
		// find a corresponding container port. This should've been caught in FindMatchingPort, but in
		// case it isn't, just return an error.
		return nil, fmt.Errorf("container port unexpectedly not found in %s", refPodName)
	}

	// Create patch operations to add the traffic-agent sidecar
	dlog.Infof(ctx, "Injecting %s into pod %s", install.AgentContainerName, refPodName)

	var patches []patchOperation
	setGID := false
	if servicePort.TargetPort.Type == intstr.Int || svc.Spec.ClusterIP == "None" {
		patches = addInitContainer(ctx, &pod, servicePort, &appPort, patches)
		setGID = true
	} else {
		patches = hidePorts(&pod, appContainer, servicePort.TargetPort.StrVal, patches)
	}
	tpEnv := make(map[string]string)
	if env.APIPort != 0 {
		tpEnv["TELEPRESENCE_API_PORT"] = strconv.Itoa(int(env.APIPort))
	}
	patches = addTPEnv(&pod, appContainer, tpEnv, patches)
	patches, err = addAgentContainer(ctx, svc, &pod, servicePort, appContainer, &appPort, setGID, podName, podNamespace, patches)
	if err != nil {
		return nil, err
	}
	patches = addAgentVolume(&pod, patches)
	return patches, nil
}

func addInitContainer(ctx context.Context, pod *core.Pod, svcPort *core.ServicePort, appPort *core.ContainerPort, patches []patchOperation) []patchOperation {
	env := managerutil.GetEnv(ctx)
	proto := svcPort.Protocol
	if proto == "" {
		proto = appPort.Protocol
	}
	containerPort := core.ContainerPort{
		Protocol:      proto,
		ContainerPort: env.AgentPort,
	}
	container := install.InitContainer(
		env.AgentRegistry+"/"+env.AgentImage,
		containerPort,
		int(appPort.ContainerPort),
	)

	if pod.Spec.InitContainers == nil {
		patches = append(patches, patchOperation{
			Op:    "add",
			Path:  "/spec/initContainers",
			Value: []core.Container{},
		})
	} else {
		for i, container := range pod.Spec.InitContainers {
			if container.Name == install.InitContainerName {
				if i == len(pod.Spec.InitContainers)-1 {
					return patches
				}
				// If the container isn't the last one, remove it so it can be appended at the end.
				patches = append(patches, patchOperation{
					Op:   "remove",
					Path: fmt.Sprintf("/spec/initContainers/%d", i),
				})
			}
		}
	}

	return append(patches, patchOperation{
		Op:    "add",
		Path:  "/spec/initContainers/-",
		Value: container,
	})
}

func addAgentVolume(pod *core.Pod, patches []patchOperation) []patchOperation {
	for _, vol := range pod.Spec.Volumes {
		if vol.Name == install.AgentAnnotationVolumeName {
			return patches
		}
	}
	return append(patches, patchOperation{
		Op:    "add",
		Path:  "/spec/volumes/-",
		Value: install.AgentVolume(),
	})
}

// addAgentContainer creates a patch operation to add the traffic-agent container
func addAgentContainer(
	ctx context.Context,
	svc *core.Service,
	pod *core.Pod,
	svcPort *core.ServicePort,
	appContainer *core.Container,
	appPort *core.ContainerPort,
	setGID bool,
	podName, namespace string,
	patches []patchOperation,
) ([]patchOperation, error) {
	env := managerutil.GetEnv(ctx)

	refPodName := podName + "." + namespace
	for _, container := range pod.Spec.Containers {
		if container.Name == install.AgentContainerName {
			dlog.Infof(ctx, "Pod %s already has container %s", refPodName, install.AgentContainerName)
			return patches, nil
		}
	}

	dlog.Debugf(ctx, "using service %q port %q when intercepting %s",
		svc.Name,
		func() string {
			if svcPort.Name != "" {
				return svcPort.Name
			}
			return strconv.Itoa(int(svcPort.Port))
		}(), refPodName)

	agentName := ""
	if pod.OwnerReferences != nil {
	owners:
		for _, owner := range pod.OwnerReferences {
			switch owner.Kind {
			case "StatefulSet":
				// If the pod is owned by a statefulset, the workload's name is the same as the statefulset's
				agentName = owner.Name
				break owners
			case "ReplicaSet":
				// If it's owned by a replicaset, then it's the same as the deployment e.g. "my-echo-697464c6c5" -> "my-echo"
				tokens := strings.Split(owner.Name, "-")
				agentName = strings.Join(tokens[:len(tokens)-1], "-")
				break owners
			}
		}
	}
	if agentName == "" {
		// If we weren't able to find a good name for the agent from the owners, take it from the pod name
		agentName = podName
		if strings.HasSuffix(agentName, "-") {
			// Transform a generated name "my-echo-697464c6c5-" into an agent service name "my-echo"
			tokens := strings.Split(podName, "-")
			agentName = strings.Join(tokens[:len(tokens)-2], "-")
		}
	}

	proto := svcPort.Protocol
	if proto == "" {
		proto = appPort.Protocol
	}
	containerPort := core.ContainerPort{
		Protocol:      proto,
		ContainerPort: env.AgentPort,
	}
	if svcPort.TargetPort.Type == intstr.String {
		containerPort.Name = svcPort.TargetPort.StrVal
	}
	patches = append(patches, patchOperation{
		Op:   "add",
		Path: "/spec/containers/-",
		Value: install.AgentContainer(
			agentName,
			env.AgentRegistry+"/"+env.AgentImage,
			appContainer,
			containerPort,
			int(appPort.ContainerPort),
			k8sapi.GetAppProto(ctx, env.AppProtocolStrategy, svcPort),
			int(env.APIPort),
			env.ManagerNamespace,
			setGID,
		)})

	return patches, nil
}

// addTPEnv adds telepresence specific environment variables to the app container
func addTPEnv(pod *core.Pod, cn *core.Container, env map[string]string, patches []patchOperation) []patchOperation {
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
func hidePorts(pod *core.Pod, cn *core.Container, portName string, patches []patchOperation) []patchOperation {
	cns := pod.Spec.Containers
	var containerPath string
	for i := range cns {
		if &cns[i] == cn {
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

	for i, p := range cn.Ports {
		if p.Name == portName {
			hidePort(fmt.Sprintf("ports/%d/name", i))
			break
		}
	}

	probes := []*core.Probe{cn.LivenessProbe, cn.ReadinessProbe, cn.StartupProbe}
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
