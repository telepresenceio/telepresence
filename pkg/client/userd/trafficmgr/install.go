package trafficmgr

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/install/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type installer struct {
	*k8s.Cluster
}

type Installer interface {
	EnsureAgent(c context.Context,
		obj k8sapi.Workload,
		config *serviceProps,
		agentImageName string,
		telepresenceAPIPort uint16,
	) (string, string, error)
	EnsureManager(c context.Context) error
	RemoveManagerAndAgents(c context.Context, agentsOnly bool, agents []*manager.AgentInfo) error
}

func NewTrafficManagerInstaller(kc *k8s.Cluster) (Installer, error) {
	return &installer{Cluster: kc}, nil
}

const annTelepresenceActions = install.DomainPrefix + "actions"

func managerImageName(ctx context.Context) string {
	return fmt.Sprintf("%s/tel2:%s", client.GetConfig(ctx).Images.Registry(ctx), strings.TrimPrefix(client.Version(), "v"))
}

// RemoveManagerAndAgents will remove the agent from all deployments listed in the given agents slice. Unless agentsOnly is true,
// it will also remove the traffic-manager service and deployment.
// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) RemoveManagerAndAgents(c context.Context, agentsOnly bool, agents []*manager.AgentInfo) error {
	// Removes the manager and all agents from the cluster
	var errs []error
	var errsLock sync.Mutex
	addError := func(e error) {
		errsLock.Lock()
		errs = append(errs, e)
		errsLock.Unlock()
	}

	// Remove the agent from all deployments
	webhookAgentChannel := make(chan k8sapi.Object, len(agents))
	wg := sync.WaitGroup{}
	wg.Add(len(agents))
	for _, ai := range agents {
		ai := ai // pin it
		go func() {
			defer wg.Done()
			agent, err := k8sapi.GetWorkload(c, ai.Name, ai.Namespace, "")
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
				webhookAgentChannel <- agent
				return
			}
			if _, ok := ann[annTelepresenceActions]; !ok {
				webhookAgentChannel <- agent
				return
			}
			if err = ki.undoObjectMods(c, agent); err != nil {
				addError(err)
				return
			}
			if err = ki.waitForApply(c, ai.Name, ai.Namespace, agent); err != nil {
				addError(err)
			}
		}()
	}
	// wait for all agents to be removed
	wg.Wait()
	close(webhookAgentChannel)

	if !agentsOnly && len(errs) == 0 {
		// agent removal succeeded. Remove the manager resources
		if err := helm.DeleteTrafficManager(c, ki.ConfigFlags, ki.GetManagerNamespace(), false); err != nil {
			addError(err)
		}

		// roll all agents installed by webhook
		webhookWaitGroup := sync.WaitGroup{}
		webhookWaitGroup.Add(len(webhookAgentChannel))
		for agent := range webhookAgentChannel {
			go func(obj k8sapi.Object) {
				defer webhookWaitGroup.Done()
				err := ki.rolloutRestart(c, obj)
				if err != nil {
					addError(err)
				}
			}(agent)
		}
		// wait for all agents to be removed
		webhookWaitGroup.Wait()
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

// recreates "kubectl rollout restart <obj>" for obj
// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) rolloutRestart(c context.Context, obj k8sapi.Object) error {
	restartAnnotation := fmt.Sprintf(
		`{"spec": {"template": {"metadata": {"annotations": {"%srestartedAt": "%s"}}}}}`,
		install.DomainPrefix,
		time.Now().Format(time.RFC3339),
	)
	return obj.Patch(c, types.StrategicMergePatchType, []byte(restartAnnotation))
}

// Finds the Referenced Service in an objects' annotations
// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) getSvcFromObjAnnotation(c context.Context, obj k8sapi.Object) (k8sapi.Object, error) {
	var actions workloadActions
	annotationsFound, err := getAnnotation(obj, &actions)
	if err != nil {
		return nil, err
	}
	namespace := obj.GetNamespace()
	if !annotationsFound {
		return nil, k8sapi.ObjErrorf(obj, "annotations[%q]: annotation is not set", annTelepresenceActions)
	}
	svcName := actions.ReferencedService
	if svcName == "" {
		return nil, k8sapi.ObjErrorf(obj, "annotations[%q]: field \"ReferencedService\" is not set", annTelepresenceActions)
	}

	svc, err := k8sapi.GetService(c, svcName, namespace)
	if err != nil && !errors2.IsNotFound(err) {
		return nil, err
	}
	if svc == nil {
		return nil, k8sapi.ObjErrorf(obj, `annotations[%q]: field \"ReferencedService\" references unfound service %s.%s`,
			annTelepresenceActions, svcName, namespace)
	}
	return svc, nil
}

// Determines if the service associated with a pre-existing intercept exists or if
// the port to-be-intercepted has changed. It raises an error if either of these
// cases exist since to go forward with an intercept would require changing the
// configuration of the agent.
// Deprecated: not used with traffic-manager versions >= 2.6.0
func checkSvcSame(_ context.Context, obj k8sapi.Object, svcName, portNameOrNumber string) error {
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
			return k8sapi.ObjErrorf(obj, "associated Service changed from %q to %q", curSvc, svcName)
		}

		// If the portNameOrNumber passed in doesn't match the referenced service
		// port name or number in the annotation, then the servicePort to be intercepted
		// has changed.
		if portNameOrNumber != "" {
			curSvcPortName := actions.ReferencedServicePortName
			curSvcPort := actions.ReferencedServicePort
			if curSvcPortName != portNameOrNumber && curSvcPort != portNameOrNumber {
				return k8sapi.ObjErrorf(obj, "port changed from %q to %q", curSvcPort, portNameOrNumber)
			}
		}
	}
	return nil
}

var agentNotFound = errors.New("no such agent")

// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) ensureInjectedAgent(
	c context.Context,
	svc *core.Service,
	name, namespace string,
	podTemplate *core.PodTemplateSpec,
	obj k8sapi.Object,
) error {
	a := podTemplate.ObjectMeta.Annotations
	webhookInjected := a != nil && a[install.InjectAnnotation] == "enabled"

	// Find pod from svc.
	// On fail, assume agent not present and roll (only if injected via webhook; rolling a manually managed deployment will do no good)
	pods, err := k8sapi.Pods(c, namespace, svc.Spec.Selector)
	if err != nil || len(pods) == 0 {
		if webhookInjected {
			dlog.Warnf(c, "Error finding pod for %s, rolling and proceeding anyway: %v", name, err)
			err = ki.rolloutRestart(c, obj)
			if err != nil {
				return err
			}
			return nil
		} else {
			return err
		}
	}

	// Check pods for agent. If missing and webhookInjected, roll pod
	roll := true
nextPod:
	for _, pod := range pods {
		podImpl, _ := k8sapi.PodImpl(pod)
		for _, container := range podImpl.Spec.Containers {
			if container.Name == install.AgentContainerName {
				roll = false
				break nextPod
			}
		}
	}
	if roll {
		if webhookInjected {
			err = ki.rolloutRestart(c, obj)
			if err != nil {
				return err
			}
		} else {
			// The user claims to have manually added the agent but we can't find it; report the error.
			return fmt.Errorf("the %s annotation is set but no traffic agent was found in %s", install.ManualInjectAnnotation, name)
		}
	}
	return nil
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func useAutoInstall(podTpl *core.PodTemplateSpec) (bool, error) {
	a := podTpl.ObjectMeta.Annotations
	webhookInjected := a != nil && a[install.InjectAnnotation] == "enabled"
	manuallyManaged := a != nil && a[install.ManualInjectAnnotation] == "true"
	if webhookInjected && manuallyManaged {
		return false, fmt.Errorf("workload is misconfigured; only one of %s and %s may be set at a time, but both are",
			install.InjectAnnotation, install.ManualInjectAnnotation)
	}
	return !(webhookInjected || manuallyManaged), nil
}

// exploreSvc finds the matching service, its containers, and their ports
// Deprecated: not used with traffic-manager versions >= 2.6.0
func exploreSvc(c context.Context, portNameOrNumber, svcName string, obj k8sapi.Workload) (*serviceProps, error) {
	podTemplate := obj.GetPodTemplate()
	cns := podTemplate.Spec.Containers
	namespace := obj.GetNamespace()
	kind := obj.GetKind()
	name := obj.GetName()

	matchingSvc, err := install.FindMatchingService(c, portNameOrNumber, svcName, namespace, podTemplate.Labels)
	if err != nil {
		return nil, err
	}

	servicePort, container, containerPortIndex, err := install.FindMatchingPort(cns, portNameOrNumber, matchingSvc)
	if err != nil {
		return nil, k8sapi.ObjErrorf(obj, err.Error())
	}

	if err := checkSvcSame(c, obj, svcName, portNameOrNumber); err != nil {
		msg := fmt.Sprintf(
			`%s already being used for intercept with a different service
configuration. To intercept this with your new configuration, please use
telepresence uninstall --agent %s This will cancel any intercepts that
already exist for this service`, kind, name)
		return nil, errors.Wrap(err, msg)
	}

	return &serviceProps{
		service:            matchingSvc,
		servicePort:        servicePort,
		workload:           obj,
		container:          container,
		containerPortIndex: containerPortIndex,
	}, nil
}

// EnsureAgent does a lot of things but at a high level it ensures that the traffic agent
// is installed alongside the proper workload. In doing that, it also ensures that
// the workload is referenced by a service. Lastly, it returns the service UID
// associated with the workload since this is where that correlation is made.
// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) EnsureAgent(
	c context.Context,
	obj k8sapi.Workload,
	svcProps *serviceProps,
	agentImageName string,
	telepresenceAPIPort uint16,
) (string, string, error) {
	podTemplate := obj.GetPodTemplate()
	kind := obj.GetKind()
	name := obj.GetName()
	namespace := obj.GetNamespace()
	rf := reflect.ValueOf(obj).Elem()
	dlog.Debugf(c, "%s %s.%s %s.%s", kind, name, namespace, rf.Type().PkgPath(), rf.Type().Name())

	autoInstall, err := useAutoInstall(podTemplate)
	if err != nil {
		return "", "", err
	}

	if !autoInstall {
		err := ki.ensureInjectedAgent(c, svcProps.service, name, namespace, podTemplate, obj)
		if err != nil {
			return "", "", err
		}
		return string(svcProps.service.GetUID()), kind, nil
	}

	var agentContainer *core.Container
	for i := range podTemplate.Spec.Containers {
		container := &podTemplate.Spec.Containers[i]
		if container.Name == install.AgentContainerName {
			agentContainer = container
			break
		}
	}

	update := true
	updateSvc := false
	switch {
	case agentContainer == nil:
		dlog.Infof(c, "no agent found for %s %s.%s", kind, name, namespace)
		obj, updateSvc, err = addAgentToWorkload(c, svcProps, agentImageName, ki.GetManagerNamespace(), telepresenceAPIPort, obj)
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
			return "", "", k8sapi.ObjErrorf(obj, "annotations[%q]: annotation is not set", annTelepresenceActions)
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
		update = false
	}

	svc := k8sapi.Service(svcProps.service)
	if update {
		if err = obj.Update(c); err != nil {
			return "", "", err
		}
		if updateSvc {
			if err := svc.Update(c); err != nil {
				return "", "", err
			}
		}
		if err := ki.waitForApply(c, name, namespace, obj); err != nil {
			return "", "", err
		}
	}

	if svc == nil {
		// If the service is still nil, that's because an agent already exists that we can reuse.
		// So we get the service from the deployments annotation so that we can extract the UID.
		svc, err = ki.getSvcFromObjAnnotation(c, obj)
		if err != nil {
			return "", "", err
		}
	}
	return string(svc.GetUID()), kind, nil
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) waitForApply(c context.Context, name, namespace string, obj k8sapi.Workload) error {
	tos := &client.GetConfig(c).Timeouts
	c, cancel := tos.TimeoutContext(c, client.TimeoutApply)
	defer cancel()

	origGeneration := int64(0)
	if obj != nil {
		origGeneration = obj.GetGeneration()
	}

	var err error
	if rs, ok := k8sapi.ReplicaSetImpl(obj); ok {
		if err = ki.refreshReplicaSet(c, namespace, rs); err != nil {
			return err
		}
	}
	for {
		dtime.SleepWithContext(c, time.Second)
		if err = c.Err(); err != nil {
			return err
		}

		if err = obj.Refresh(c); err != nil {
			return client.CheckTimeout(c, err)
		}
		if obj.Updated(origGeneration) {
			dlog.Debugf(c, "%s %s.%s successfully applied", obj.GetKind(), name, namespace)
			return nil
		}
	}
}

// refreshReplicaSet finds pods owned by a given ReplicaSet and deletes them.
// We need this because updating a Replica Set does *not* generate new
// pods if the desired amount already exists.
// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) refreshReplicaSet(c context.Context, namespace string, rs *apps.ReplicaSet) error {
	pods, err := k8sapi.Pods(c, namespace, rs.Spec.Selector.MatchLabels)
	if err != nil {
		return err
	}

	for _, pod := range pods {
		podImpl, _ := k8sapi.PodImpl(pod)
		for _, ownerRef := range podImpl.OwnerReferences {
			if ownerRef.UID == rs.UID {
				dlog.Infof(c, "Deleting pod %s.%s owned by rs %s", podImpl.Name, podImpl.Namespace, rs.Name)
				if err = pod.Delete(c); err != nil {
					if errors2.IsNotFound(err) || errors2.IsConflict(err) {
						// If an intercept creates a new pod by installing an agent, and the agent is then uninstalled shortly after, the
						// old pod may still show up here during removal, and even after it has been removed if the removal completed
						// after we obtained the pods list. This is OK. This pod will not be in our way.
						continue
					}
				}
			}
		}
	}
	return nil
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func getAnnotation(obj k8sapi.Object, data completeAction) (bool, error) {
	ann := obj.GetAnnotations()
	if ann == nil {
		return false, nil
	}
	ajs, ok := ann[annTelepresenceActions]
	if !ok {
		return false, nil
	}
	if err := data.UnmarshalAnnotation(ajs); err != nil {
		return false, k8sapi.ObjErrorf(obj, "annotations[%q]: unable to parse annotation: %q: %w",
			annTelepresenceActions, ajs, err)
	}

	annV, err := data.TelVersion()
	if err != nil {
		return false, k8sapi.ObjErrorf(obj, "annotations[%q]: unable to parse semantic version %q: %w",
			annTelepresenceActions, ajs, err)
	}
	ourV := client.Semver()

	// Compare major and minor versions. 100% backward compatibility is assumed and greater patch versions are allowed
	if ourV.Major < annV.Major || ourV.Major == annV.Major && ourV.Minor < annV.Minor {
		return false, k8sapi.ObjErrorf(obj, "annotations[%q]: the version in the annotation (%v) is more recent than this binary's version (%v)",
			annTelepresenceActions,
			annV, ourV)
	}
	return true, nil
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) undoObjectMods(c context.Context, obj k8sapi.Object) error {
	referencedService, err := undoObjectMods(c, obj)
	if err != nil {
		return err
	}
	svc, err := k8sapi.GetService(c, referencedService, obj.GetNamespace())
	if err != nil && !errors2.IsNotFound(err) {
		return err
	}
	if svc != nil {
		if err = ki.undoServiceMods(c, svc); err != nil {
			return err
		}
	}
	return obj.Update(c)
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func undoObjectMods(c context.Context, obj k8sapi.Object) (string, error) {
	var actions workloadActions
	ok, err := getAnnotation(obj, &actions)
	if !ok {
		return "", err
	}
	if !ok {
		return "", k8sapi.ObjErrorf(obj, "agent is not installed")
	}

	if err = actions.Undo(obj); err != nil {
		if install.IsAlreadyUndone(err) {
			dlog.Warnf(c, "Already uninstalled: %v", err)
		} else {
			return "", err
		}
	}
	mObj := obj.(meta.ObjectMetaAccessor).GetObjectMeta()
	annotations := mObj.GetAnnotations()
	delete(annotations, annTelepresenceActions)
	if len(annotations) == 0 {
		mObj.SetAnnotations(nil)
	}
	explainUndo(c, &actions, obj)
	return actions.ReferencedService, nil
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func (ki *installer) undoServiceMods(c context.Context, svc k8sapi.Object) (err error) {
	if err = undoServiceMods(c, svc); err == nil {
		err = svc.Update(c)
	}
	return err
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func undoServiceMods(c context.Context, svc k8sapi.Object) error {
	var actions svcActions
	ok, err := getAnnotation(svc, &actions)
	if !ok {
		return err
	}
	if err = actions.Undo(svc); err != nil {
		if install.IsAlreadyUndone(err) {
			dlog.Warnf(c, "Already uninstalled: %v", err)
		} else {
			return err
		}
	}
	anns := svc.GetAnnotations()
	delete(anns, annTelepresenceActions)
	if len(anns) == 0 {
		anns = nil
	}
	svc.SetAnnotations(anns)
	explainUndo(c, &actions, svc)
	return nil
}

// addAgentToWorkload takes a given workload object and a service and
// determines which container + port to use for an intercept. It also
// prepares and performs modifications to the obj and/or service.
// Deprecated: not used with traffic-manager versions >= 2.6.0
func addAgentToWorkload(
	c context.Context,
	svcProps *serviceProps,
	agentImageName string,
	trafficManagerNamespace string,
	telepresenceAPIPort uint16,
	object k8sapi.Workload,
) (
	k8sapi.Workload,
	bool,
	error,
) {
	matchingService := svcProps.service
	container := svcProps.container
	servicePort := svcProps.servicePort
	containerPortIndex := svcProps.containerPortIndex

	dlog.Debugf(c, "using service %q port %q when intercepting %s %s",
		matchingService.Name,
		func() string {
			if svcProps.servicePort.Name != "" {
				return svcProps.servicePort.Name
			}
			return strconv.Itoa(int(svcProps.servicePort.Port))
		}(),
		object.GetKind(),
		nameAndNamespace(object))

	version := client.Semver().String()

	// Try to detect the container port we'll be taking over.
	var containerPort struct {
		Name     string // If the existing container port doesn't have a name, we'll make one up.
		Number   uint16
		Protocol core.Protocol
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
		return nil, false, k8sapi.ObjErrorf(object, "unable to add: the container port cannot be determined")
	}
	if containerPort.Name == "" {
		containerPort.Name = fmt.Sprintf("tx-%d", containerPort.Number)
	}

	var initContainerAction *addInitContainerAction
	if matchingService.Spec.ClusterIP == "None" {
		initContainerAction = &addInitContainerAction{
			AppPortProto:  containerPort.Protocol,
			AppPortNumber: containerPort.Number,
			ImageName:     agentImageName,
		}
	}

	var addTPEnvAction *addTPEnvironmentAction
	if telepresenceAPIPort != 0 {
		addTPEnvAction = &addTPEnvironmentAction{
			ContainerName: container.Name,
			Env:           map[string]string{"TELEPRESENCE_API_PORT": strconv.Itoa(int(telepresenceAPIPort))},
		}
	}

	// Figure what modifications we need to make.
	workloadMod := &workloadActions{
		Version:                   version,
		ReferencedService:         matchingService.Name,
		ReferencedServicePort:     strconv.Itoa(int(servicePort.Port)),
		ReferencedServicePortName: servicePort.Name,
		AddInitContainer:          initContainerAction,
		AddTrafficAgent: &addTrafficAgentAction{
			containerName:           container.Name,
			trafficManagerNamespace: trafficManagerNamespace,
			ContainerPortName:       containerPort.Name,
			ContainerPortProto:      containerPort.Protocol,
			ContainerPortAppProto:   k8sapi.GetAppProto(c, client.GetConfig(c).Intercept.AppProtocolStrategy, servicePort),
			ContainerPortNumber:     containerPort.Number,
			APIPortNumber:           telepresenceAPIPort,
			ImageName:               agentImageName,
		},
		AddTPEnvironmentAction: addTPEnvAction,
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
	var err error
	if err = workloadMod.Do(object); err != nil {
		return nil, false, err
	}
	mObj := object.(meta.ObjectMetaAccessor).GetObjectMeta()
	annotations := mObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annTelepresenceActions], err = workloadMod.MarshalAnnotation()
	if err != nil {
		return nil, false, err
	}
	mObj.SetAnnotations(annotations)
	explainDo(c, workloadMod, object)

	// Apply the actions on the Service.
	updateService := false
	svc := k8sapi.Service(matchingService)
	if serviceMod != nil {
		if err = serviceMod.Do(svc); err != nil {
			return nil, false, err
		}
		if matchingService.Annotations == nil {
			matchingService.Annotations = make(map[string]string)
		}
		matchingService.Annotations[annTelepresenceActions], err = serviceMod.MarshalAnnotation()
		if err != nil {
			return nil, false, err
		}
		explainDo(c, serviceMod, k8sapi.Service(matchingService))
		updateService = true
	}

	return object, updateService, nil
}

func (ki *installer) EnsureManager(c context.Context) error {
	return helm.EnsureTrafficManager(c, ki.ConfigFlags, ki.GetManagerNamespace())
}
