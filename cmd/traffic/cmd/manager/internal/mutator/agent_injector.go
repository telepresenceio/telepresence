package mutator

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	admission "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

var podResource = metav1.GroupVersionResource{Version: "v1", Group: "", Resource: "pods"}
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
	pod := corev1.Pod{}
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
		dlog.Infof(ctx, "Unable to extract pod name and/or namespace (got %q); skipping", refPodName)
		return nil, nil
	}

	if pod.Annotations[install.InjectAnnotation] != "enabled" {
		dlog.Infof(ctx, `The %s pod has not enabled %s container injection through %q annotation; skipping`,
			refPodName, install.AgentContainerName, install.InjectAnnotation)
		return nil, nil
	}

	for _, container := range pod.Spec.Containers {
		if container.Name == install.AgentContainerName {
			dlog.Infof(ctx, "The %s pod already has a %q container; skipping", refPodName, install.AgentContainerName)
			return nil, nil
		}
	}

	svc, err := findMatchingService(ctx, managerutil.GetKatesClient(ctx), "", "", podNamespace, pod.Labels)
	if err != nil {
		dlog.Error(ctx, err)
		return nil, nil
	}

	// The ServicePortAnnotation is expected to contain a string that identifies the service port.
	portNameOrNumber := pod.Annotations[install.ServicePortAnnotation]
	servicePort, appContainer, containerPortIndex, err := install.FindMatchingPort(pod.Spec.Containers, portNameOrNumber, svc)
	if err != nil {
		dlog.Error(ctx, err)
		return nil, nil
	}

	env := managerutil.GetEnv(ctx)
	ports := appContainer.Ports
	for i := range ports {
		if ports[i].ContainerPort == env.AgentPort {
			dlog.Infof(ctx, "the %s pod container is exposing the same port (%d) as the %s sidecar; skipping",
				refPodName, env.AgentPort, install.AgentContainerName)
			return nil, nil
		}
	}

	if svc.Spec.ClusterIP == "None" {
		return nil, fmt.Errorf("intercepts of headless service: %s.%s won't work "+
			"see https://github.com/telepresenceio/telepresence/issues/1632",
			svc.Name, svc.Namespace)
	}

	if servicePort.TargetPort.Type == intstr.Int {
		return nil, fmt.Errorf("intercepts of service %s.%s won't work because it has an integer targetPort",
			svc.Name, svc.Namespace)
	}

	appPort := appContainer.Ports[containerPortIndex]

	// Create patch operations to add the traffic-agent sidecar
	dlog.Infof(ctx, "Injecting %s into pod %s", install.AgentContainerName, refPodName)

	var patches []patchOperation
	patches, err = addAgentContainer(ctx, svc, servicePort, appContainer, &appPort, podName, podNamespace, patches)
	if err != nil {
		return nil, err
	}
	patches = hidePorts(&pod, appContainer, servicePort.TargetPort.StrVal, patches)
	patches = addAgentVolume(patches)
	return patches, nil
}

func addAgentVolume(patches []patchOperation) []patchOperation {
	return append(patches, patchOperation{
		Op:    "add",
		Path:  "/spec/volumes/-",
		Value: install.AgentVolume(),
	})
}

// addAgentContainer creates a patch operation to add the traffic-agent container
func addAgentContainer(
	ctx context.Context,
	svc *corev1.Service,
	svcPort *corev1.ServicePort,
	appContainer *corev1.Container,
	appPort *corev1.ContainerPort,
	podName, namespace string,
	patches []patchOperation) ([]patchOperation, error) {
	env := managerutil.GetEnv(ctx)

	refPodName := podName + "." + namespace
	dlog.Debugf(ctx, "using service %q port %q when intercepting %s",
		svc.Name,
		func() string {
			if svcPort.Name != "" {
				return svcPort.Name
			}
			return strconv.Itoa(int(svcPort.Port))
		}(), refPodName)

	agentName := podName
	if strings.HasSuffix(agentName, "-") {
		// Transform a generated name "my-echo-697464c6c5-" into an agent service name "my-echo"
		tokens := strings.Split(podName, "-")
		agentName = strings.Join(tokens[:len(tokens)-2], "-")
	}

	proto := svcPort.Protocol
	if proto == "" {
		proto = appPort.Protocol
	}
	patches = append(patches, patchOperation{
		Op:   "add",
		Path: "/spec/containers/-",
		Value: install.AgentContainer(
			agentName,
			env.AgentImage,
			appContainer,
			corev1.ContainerPort{
				Name:          svcPort.TargetPort.StrVal,
				Protocol:      proto,
				ContainerPort: env.AgentPort,
			},
			int(appPort.ContainerPort),
			env.ManagerNamespace)})

	return patches, nil
}

// hidePorts  will replace the symbolic name of a container port with a generated name. It will perform
// the same replacement on all references to that port from the probes of the container
func hidePorts(pod *corev1.Pod, cn *corev1.Container, portName string, patches []patchOperation) []patchOperation {
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
			Path:  fmt.Sprintf("%s/%s/name", containerPath, path),
			Value: hiddenPortName,
		})
	}

	for i, p := range cn.Ports {
		if p.Name == portName {
			hidePort(fmt.Sprintf("ports/%d", i))
			break
		}
	}

	probes := []*corev1.Probe{cn.LivenessProbe, cn.ReadinessProbe, cn.StartupProbe}
	probeNames := []string{"/livenessProbe", "/readinessProbe", "/startupProbe"}

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
