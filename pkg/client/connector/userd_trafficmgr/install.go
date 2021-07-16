package userd_trafficmgr

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/install/resource"
)

type installer struct {
	*userd_k8s.Cluster
}

func newTrafficManagerInstaller(kc *userd_k8s.Cluster) (*installer, error) {
	return &installer{Cluster: kc}, nil
}

const annTelepresenceActions = install.DomainPrefix + "actions"

func managerImageName(ctx context.Context) string {
	return fmt.Sprintf("%s/tel2:%s", client.GetConfig(ctx).Images.Registry, strings.TrimPrefix(client.Version(), "v"))
}

// removeManager will remove the agent from all deployments listed in the given agents slice. Unless agentsOnly is true,
// it will also remove the traffic-manager service and deployment.
func (ki *installer) removeManagerAndAgents(c context.Context, agentsOnly bool, agents []*manager.AgentInfo, env *client.Env) error {
	// Removes the manager and all agents from the cluster
	var errs []error
	var errsLock sync.Mutex
	addError := func(e error) {
		errsLock.Lock()
		errs = append(errs, e)
		errsLock.Unlock()
	}

	// Remove the agent from all deployments
	wg := sync.WaitGroup{}
	wg.Add(len(agents))
	for _, ai := range agents {
		ai := ai // pin it
		go func() {
			defer wg.Done()
			agent, err := ki.FindWorkload(c, ai.Namespace, ai.Name)
			if err != nil {
				if !errors2.IsNotFound(err) {
					addError(err)
				}
				return
			}

			// Assume that the agent was added using the mutating webhook when no actions
			// annotation can be found in the workload.
			ann := agent.GetAnnotations()
			if ann == nil {
				return
			}
			if _, ok := ann[annTelepresenceActions]; !ok {
				return
			}
			if err = ki.undoObjectMods(c, agent); err != nil {
				addError(err)
				return
			}
			if err = ki.waitForApply(c, ai.Namespace, ai.Name, agent); err != nil {
				addError(err)
			}
		}()
	}
	// wait for all agents to be removed
	wg.Wait()

	if !agentsOnly && len(errs) == 0 {
		// agent removal succeeded. Remove the manager resources
		if err := resource.DeleteTrafficManager(c, ki.Client(), ki.GetManagerNamespace(), env); err != nil {
			addError(err)
		}
	}

	switch len(errs) {
	case 0:
	case 1:
		return errs[0]
	default:
		bld := bytes.NewBufferString("multiple errors:")
		for _, err := range errs {
			bld.WriteString("\n  ")
			bld.WriteString(err.Error())
		}
		return errors.New(bld.String())
	}
	return nil
}

// Finds the Referenced Service in an objects' annotations
func (ki *installer) getSvcFromObjAnnotation(c context.Context, obj kates.Object) (*kates.Service, error) {
	var actions workloadActions
	annotationsFound, err := getAnnotation(obj, &actions)
	if err != nil {
		return nil, err
	}
	namespace := obj.GetNamespace()
	if !annotationsFound {
		return nil, install.ObjErrorf(obj, "annotations[%q]: annotation is not set", annTelepresenceActions)
	}
	svcName := actions.ReferencedService
	if svcName == "" {
		return nil, install.ObjErrorf(obj, "annotations[%q]: field \"ReferencedService\" is not set", annTelepresenceActions)
	}

	svc, err := ki.FindSvc(c, namespace, svcName)
	if err != nil && !kates.IsNotFound(err) {
		return nil, err
	}
	if svc == nil {
		return nil, install.ObjErrorf(obj, "annotations[%q]: field \"ReferencedService\" references unfound service %q", annTelepresenceActions, svcName)
	}
	return svc, nil
}

// Determines if the service associated with a pre-existing intercept exists or if
// the port to-be-intercepted has changed. It raises an error if either of these
// cases exist since to go forward with an intercept would require changing the
// configuration of the agent.
func checkSvcSame(c context.Context, obj kates.Object, svcName, portNameOrNumber string) error {
	var actions workloadActions
	annotationsFound, err := getAnnotation(obj, &actions)
	if err != nil {
		return err
	}
	if annotationsFound {
		// If the Service in the annotation doesn't match the svcName passed in
		// then the service to be used with the intercept has changed
		curSvc := actions.ReferencedService
		if svcName != "" && curSvc != svcName {
			return install.ObjErrorf(obj, "associated Service changed from %q to %q", curSvc, svcName)
		}

		// If the portNameOrNumber passed in doesn't match the referenced service
		// port name or number in the annotation, then the servicePort to be intercepted
		// has changed.
		if portNameOrNumber != "" {
			curSvcPortName := actions.ReferencedServicePortName
			curSvcPort := actions.ReferencedServicePort
			if curSvcPortName != portNameOrNumber && curSvcPort != portNameOrNumber {
				return install.ObjErrorf(obj, "port changed from %q to %q", curSvcPort, portNameOrNumber)
			}
		}
	}
	return nil
}

var agentNotFound = errors.New("no such agent")

// This does a lot of things but at a high level it ensures that the traffic agent
// is installed alongside the proper workload. In doing that, it also ensures that
// the workload is referenced by a service. Lastly, it returns the service UID
// associated with the workload since this is where that correlation is made.
func (ki *installer) ensureAgent(c context.Context, namespace, name, svcName, portNameOrNumber, agentImageName string) (string, string, error) {
	obj, err := ki.FindWorkload(c, namespace, name)
	if err != nil {
		return "", "", err
	}
	podTemplate, err := install.GetPodTemplateFromObject(obj)
	if err != nil {
		return "", "", err
	}

	kind := obj.GetObjectKind().GroupVersionKind().Kind

	var svc *kates.Service
	if a := podTemplate.ObjectMeta.Annotations; a != nil && a[install.InjectAnnotation] == "enabled" {
		// agent is injected using a mutating webhook. Get its service and skip the rest
		svc, err = install.FindMatchingService(c, ki.Client(), portNameOrNumber, svcName, namespace, podTemplate.Labels)
		if err != nil {
			return "", "", err
		}
		return string(svc.GetUID()), kind, nil
	}

	var agentContainer *kates.Container
	for i := range podTemplate.Spec.Containers {
		container := &podTemplate.Spec.Containers[i]
		if container.Name == install.AgentContainerName {
			agentContainer = container
			break
		}
	}

	if err := checkSvcSame(c, obj, svcName, portNameOrNumber); err != nil {
		msg := fmt.Sprintf(
			`%s already being used for intercept with a different service
configuration. To intercept this with your new configuration, please use
telepresence uninstall --agent %s This will cancel any intercepts that
already exist for this service`, kind, obj.GetName())
		return "", "", errors.Wrap(err, msg)
	}

	switch {
	case agentContainer == nil:
		dlog.Infof(c, "no agent found for %s %s.%s", kind, name, namespace)
		dlog.Infof(c, "Using port name or number %q", portNameOrNumber)
		matchingSvc, err := install.FindMatchingService(c, ki.Client(), portNameOrNumber, svcName, namespace, podTemplate.Labels)
		if err != nil {
			return "", "", err
		}
		obj, svc, err = addAgentToWorkload(c, portNameOrNumber, agentImageName, ki.GetManagerNamespace(), obj, matchingSvc)
		if err != nil {
			return "", "", err
		}
	case agentContainer.Image != agentImageName:
		var actions workloadActions
		ok, err := getAnnotation(obj, &actions)
		if err != nil {
			return "", "", err
		} else if !ok {
			// This can only happen if someone manually tampered with the annTelepresenceActions annotation
			return "", "", install.ObjErrorf(obj, "annotations[%q]: annotation is not set", annTelepresenceActions)
		}

		dlog.Debugf(c, "Updating agent for %s %s.%s", kind, name, namespace)
		aaa := &workloadActions{
			Version:         actions.Version,
			AddTrafficAgent: actions.AddTrafficAgent,
		}
		explainUndo(c, aaa, obj)
		aaa.AddTrafficAgent.ImageName = agentImageName
		agentContainer.Image = agentImageName
		explainDo(c, aaa, obj)
	default:
		dlog.Debugf(c, "%s %s.%s already has an installed and up-to-date agent", kind, name, namespace)
	}

	if err := ki.Client().Update(c, obj, obj); err != nil {
		return "", "", err
	}
	if svc != nil {
		if err := ki.Client().Update(c, svc, svc); err != nil {
			return "", "", err
		}
	} else {
		// If the service is still nil, that's because an agent already exists that we can reuse.
		// So we get the service from the deployments annotation so that we can extract the UID.
		svc, err = ki.getSvcFromObjAnnotation(c, obj)
		if err != nil {
			return "", "", err
		}
	}

	if err := ki.waitForApply(c, namespace, name, obj); err != nil {
		return "", "", err
	}
	return string(svc.GetUID()), kind, nil
}

// The following <workload>Updated functions all contain the logic for
// determining if that specific workload type has successfully been updated
// based on the object's metadata. We have separate ones for each object
// because the criteria is slightly different for each.
func replicaSetUpdated(rs *kates.ReplicaSet, origGeneration int64) bool {
	applied := rs.ObjectMeta.Generation >= origGeneration &&
		rs.Status.ObservedGeneration == rs.ObjectMeta.Generation &&
		(rs.Spec.Replicas == nil || rs.Status.Replicas >= *rs.Spec.Replicas) &&
		rs.Status.FullyLabeledReplicas == rs.Status.Replicas &&
		rs.Status.AvailableReplicas == rs.Status.Replicas
	return applied
}

func deploymentUpdated(dep *kates.Deployment, origGeneration int64) bool {
	applied := dep.ObjectMeta.Generation >= origGeneration &&
		dep.Status.ObservedGeneration == dep.ObjectMeta.Generation &&
		(dep.Spec.Replicas == nil || dep.Status.UpdatedReplicas >= *dep.Spec.Replicas) &&
		dep.Status.UpdatedReplicas == dep.Status.Replicas &&
		dep.Status.AvailableReplicas == dep.Status.Replicas
	return applied
}

func statefulSetUpdated(statefulSet *kates.StatefulSet, origGeneration int64) bool {
	applied := statefulSet.ObjectMeta.Generation >= origGeneration &&
		statefulSet.Status.ObservedGeneration == statefulSet.ObjectMeta.Generation &&
		(statefulSet.Spec.Replicas == nil || statefulSet.Status.UpdatedReplicas >= *statefulSet.Spec.Replicas) &&
		statefulSet.Status.UpdatedReplicas == statefulSet.Status.Replicas &&
		statefulSet.Status.CurrentReplicas == statefulSet.Status.Replicas
	return applied
}

func (ki *installer) waitForApply(c context.Context, namespace, name string, obj kates.Object) error {
	tos := &client.GetConfig(c).Timeouts
	c, cancel := tos.TimeoutContext(c, client.TimeoutApply)
	defer cancel()

	origGeneration := int64(0)
	if obj != nil {
		origGeneration = obj.GetGeneration()
	}

	var err error
	if rs, ok := obj.(*kates.ReplicaSet); ok {
		if err = ki.refreshReplicaSet(c, name, namespace, rs); err != nil {
			return err
		}
	}
	for {
		dtime.SleepWithContext(c, time.Second)
		if err = c.Err(); err != nil {
			return err
		}

		if obj, err = ki.FindAgain(c, obj); err != nil {
			return client.CheckTimeout(c, err)
		}

		updated := false
		switch obj := obj.(type) {
		case *kates.ReplicaSet:
			updated = replicaSetUpdated(obj, origGeneration)
		case *kates.Deployment:
			updated = deploymentUpdated(obj, origGeneration)
		case *kates.StatefulSet:
			updated = statefulSetUpdated(obj, origGeneration)
		}
		if updated {
			dlog.Debugf(c, "%s %s.%s successfully applied", obj.GetObjectKind().GroupVersionKind().Kind, name, namespace)
			return nil
		}
	}
}

// refreshReplicaSet finds pods owned by a given ReplicaSet and deletes them.
// We need this because updating a Replica Set does *not* generate new
// pods if the desired amount already exists.
func (ki *installer) refreshReplicaSet(c context.Context, name, namespace string, rs *kates.ReplicaSet) error {
	podNames, err := ki.PodNames(c, namespace)
	if err != nil {
		return err
	}

	for _, podName := range podNames {
		// We only care about pods that are associated with the ReplicaSet
		// so we filter them out here
		if !strings.Contains(podName, name) {
			continue
		}
		podInfo, err := ki.FindPod(c, namespace, podName)
		if err != nil {
			return err
		}

		for _, ownerRef := range podInfo.OwnerReferences {
			if ownerRef.UID == rs.UID {
				dlog.Infof(c, "Deleting pod %s owned by rs %s", podInfo.Name, rs.Name)
				pod := &kates.Pod{
					TypeMeta: kates.TypeMeta{
						Kind: "Pod",
					},
					ObjectMeta: kates.ObjectMeta{
						Namespace: podInfo.Namespace,
						Name:      podInfo.Name,
					},
				}
				if err := ki.Client().Delete(c, pod, pod); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func getAnnotation(obj kates.Object, data completeAction) (bool, error) {
	ann := obj.GetAnnotations()
	if ann == nil {
		return false, nil
	}
	ajs, ok := ann[annTelepresenceActions]
	if !ok {
		return false, nil
	}
	if err := data.UnmarshalAnnotation(ajs); err != nil {
		return false, install.ObjErrorf(obj, "annotations[%q]: unable to parse annotation: %q: %w",
			annTelepresenceActions, ajs, err)
	}

	annV, err := data.TelVersion()
	if err != nil {
		return false, install.ObjErrorf(obj, "annotations[%q]: unable to parse semantic version %q: %w",
			annTelepresenceActions, ajs, err)
	}
	ourV := client.Semver()

	// Compare major and minor versions. 100% backward compatibility is assumed and greater patch versions are allowed
	if ourV.Major < annV.Major || ourV.Major == annV.Major && ourV.Minor < annV.Minor {
		return false, install.ObjErrorf(obj, "annotations[%q]: the version in the annotation (%v) is more recent than this binary's version (%v)",
			annTelepresenceActions,
			annV, ourV)
	}
	return true, nil
}

func (ki *installer) undoObjectMods(c context.Context, obj kates.Object) error {
	referencedService, err := undoObjectMods(c, obj)
	if err != nil {
		return err
	}
	svc, err := ki.FindSvc(c, obj.GetNamespace(), referencedService)
	if err != nil && !kates.IsNotFound(err) {
		return err
	}
	if svc != nil {
		if err = ki.undoServiceMods(c, svc); err != nil {
			return err
		}
	}
	return ki.Client().Update(c, obj, obj)
}

func undoObjectMods(c context.Context, obj kates.Object) (string, error) {
	var actions workloadActions
	ok, err := getAnnotation(obj, &actions)
	if !ok {
		return "", err
	}
	if !ok {
		return "", install.ObjErrorf(obj, "agent is not installed")
	}

	if err = actions.Undo(obj); err != nil {
		return "", err
	}
	annotations := obj.GetAnnotations()
	delete(annotations, annTelepresenceActions)
	if len(annotations) == 0 {
		obj.SetAnnotations(nil)
	}
	explainUndo(c, &actions, obj)
	return actions.ReferencedService, nil
}

func (ki *installer) undoServiceMods(c context.Context, svc *kates.Service) error {
	if err := undoServiceMods(c, svc); err != nil {
		return err
	}
	return ki.Client().Update(c, svc, svc)
}

func undoServiceMods(c context.Context, svc *kates.Service) error {
	var actions svcActions
	ok, err := getAnnotation(svc, &actions)
	if !ok {
		return err
	}
	if err = actions.Undo(svc); err != nil {
		return err
	}
	delete(svc.Annotations, annTelepresenceActions)
	if len(svc.Annotations) == 0 {
		svc.Annotations = nil
	}
	explainUndo(c, &actions, svc)
	return nil
}

// addAgentToWorkload takes a given workload object and a service and
// determines which container + port to use for an intercept. It also
// prepares and performs modifications to the obj and/or service.
func addAgentToWorkload(
	c context.Context,
	portNameOrNumber string,
	agentImageName string,
	trafficManagerNamespace string,
	object kates.Object, matchingService *kates.Service,
) (
	kates.Object,
	*kates.Service,
	error,
) {
	podTemplate, err := install.GetPodTemplateFromObject(object)
	if err != nil {
		return nil, nil, err
	}

	cns := podTemplate.Spec.Containers
	servicePort, container, containerPortIndex, err := install.FindMatchingPort(cns, portNameOrNumber, matchingService)
	if err != nil {
		return nil, nil, install.ObjErrorf(object, err.Error())
	}
	if matchingService.Spec.ClusterIP == "None" {
		dlog.Debugf(c,
			"Intercepts of headless service: %s likely won't work as expected "+
				"see https://github.com/telepresenceio/telepresence/issues/1632",
			matchingService.Name)
	}
	dlog.Debugf(c, "using service %q port %q when intercepting %s %q",
		matchingService.Name,
		func() string {
			if servicePort.Name != "" {
				return servicePort.Name
			}
			return strconv.Itoa(int(servicePort.Port))
		}(),
		object.GetObjectKind().GroupVersionKind().Kind,
		object.GetName())

	version := client.Semver().String()

	// Try to detect the container port we'll be taking over.
	var containerPort struct {
		Name     string // If the existing container port doesn't have a name, we'll make one up.
		Number   uint16
		Protocol corev1.Protocol
	}

	// Start by filling from the servicePort; if these are the zero values, that's OK.
	svcHasTargetPort := true
	if servicePort.TargetPort.Type == intstr.Int {
		if servicePort.TargetPort.IntVal == 0 {
			containerPort.Number = uint16(servicePort.Port)
			svcHasTargetPort = false
		} else {
			containerPort.Number = uint16(servicePort.TargetPort.IntVal)
		}
	} else {
		containerPort.Name = servicePort.TargetPort.StrVal
	}
	containerPort.Protocol = servicePort.Protocol

	// Now fill from the Deployment's containerPort.
	usedContainerName := false
	if containerPortIndex >= 0 {
		if containerPort.Name == "" {
			containerPort.Name = container.Ports[containerPortIndex].Name
			if containerPort.Name != "" {
				usedContainerName = true
			}
		}
		if containerPort.Number == 0 {
			containerPort.Number = uint16(container.Ports[containerPortIndex].ContainerPort)
		}
		if containerPort.Protocol == "" {
			containerPort.Protocol = container.Ports[containerPortIndex].Protocol
		}
	}
	if containerPort.Number == 0 {
		return nil, nil, install.ObjErrorf(object, "unable to add: the container port cannot be determined")
	}
	if containerPort.Name == "" {
		containerPort.Name = fmt.Sprintf("tx-%d", containerPort.Number)
	}

	// Figure what modifications we need to make.
	workloadMod := &workloadActions{
		Version:                   version,
		ReferencedService:         matchingService.Name,
		ReferencedServicePort:     strconv.Itoa(int(servicePort.Port)),
		ReferencedServicePortName: servicePort.Name,
		AddTrafficAgent: &addTrafficAgentAction{
			containerName:           container.Name,
			trafficManagerNamespace: trafficManagerNamespace,
			ContainerPortName:       containerPort.Name,
			ContainerPortProto:      containerPort.Protocol,
			ContainerPortNumber:     containerPort.Number,
			ImageName:               agentImageName,
		},
	}
	// Depending on whether the Service refers to the port by name or by number, we either need
	// to patch the names in the deployment, or the number in the service.
	var serviceMod *svcActions
	if servicePort.TargetPort.Type == intstr.Int {
		// Change the port number that the Service refers to.
		serviceMod = &svcActions{Version: version}
		if svcHasTargetPort {
			serviceMod.MakePortSymbolic = &makePortSymbolicAction{
				PortName:     servicePort.Name,
				TargetPort:   containerPort.Number,
				SymbolicName: containerPort.Name,
			}
		} else {
			serviceMod.AddSymbolicPort = &addSymbolicPortAction{
				makePortSymbolicAction{
					PortName:     servicePort.Name,
					TargetPort:   containerPort.Number,
					SymbolicName: containerPort.Name,
				},
			}
		}
		// Since we are updating the service to use the containerPort.Name
		// if that value came from the container, then we need to hide it
		// since the service is using the targetPort's int.
		if usedContainerName {
			workloadMod.HideContainerPort = &hideContainerPortAction{
				ContainerName: container.Name,
				PortName:      containerPort.Name,
				ordinal:       0,
			}
		}
	} else {
		// Hijack the port name in the Deployment.
		workloadMod.HideContainerPort = &hideContainerPortAction{
			ContainerName: container.Name,
			PortName:      containerPort.Name,
			ordinal:       0,
		}
	}

	// Apply the actions on the workload.
	if err = workloadMod.Do(object); err != nil {
		return nil, nil, err
	}
	annotations := object.GetAnnotations()
	if object.GetAnnotations() == nil {
		annotations = make(map[string]string)
	}
	annotations[annTelepresenceActions], err = workloadMod.MarshalAnnotation()
	if err != nil {
		return nil, nil, err
	}
	object.SetAnnotations(annotations)
	explainDo(c, workloadMod, object)

	// Apply the actions on the Service.
	if serviceMod != nil {
		if err = serviceMod.Do(matchingService); err != nil {
			return nil, nil, err
		}
		if matchingService.Annotations == nil {
			matchingService.Annotations = make(map[string]string)
		}
		matchingService.Annotations[annTelepresenceActions], err = serviceMod.MarshalAnnotation()
		if err != nil {
			return nil, nil, err
		}
		explainDo(c, serviceMod, matchingService)
	} else {
		matchingService = nil
	}

	return object, matchingService, nil
}

func (ki *installer) ensureManager(c context.Context, env *client.Env) error {
	return resource.EnsureTrafficManager(c, ki.Client(), ki.GetManagerNamespace(), ki.GetClusterId(c), env)
}
