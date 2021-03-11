package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type installer struct {
	*k8sCluster
}

func newTrafficManagerInstaller(kc *k8sCluster) (*installer, error) {
	return &installer{k8sCluster: kc}, nil
}

const ManagerPortSSH = 8022
const ManagerPortHTTP = 8081
const managerAppName = "traffic-manager"
const telName = "manager"
const domainPrefix = "telepresence.getambassador.io/"
const annTelepresenceActions = domainPrefix + "actions"
const agentContainerName = "traffic-agent"

// this is modified in tests
var managerNamespace = func() string {
	if ns := os.Getenv("TELEPRESENCE_MANAGER_NAMESPACE"); ns != "" {
		return ns
	}
	return "ambassador"
}()

var labelMap = map[string]string{
	"app":          managerAppName,
	"telepresence": telName,
}

var (
	managerImage       string
	resolveManagerName = sync.Once{}
)

func managerImageName(env client.Env) string {
	resolveManagerName.Do(func() {
		managerImage = fmt.Sprintf("%s/tel2:%s", env.Registry, strings.TrimPrefix(client.Version(), "v"))
	})
	return managerImage
}

func (ki *installer) createManagerSvc(c context.Context) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: managerNamespace,
			Name:      managerAppName},
		Spec: kates.ServiceSpec{
			Type:      "ClusterIP",
			ClusterIP: "None",
			Selector:  labelMap,
			Ports: []kates.ServicePort{
				{
					Name: "sshd",
					Port: ManagerPortSSH,
					TargetPort: kates.IntOrString{
						Type:   intstr.String,
						StrVal: "sshd",
					},
				},
				{
					Name: "api",
					Port: ManagerPortHTTP,
					TargetPort: kates.IntOrString{
						Type:   intstr.String,
						StrVal: "api",
					},
				},
			},
		},
	}

	// Ensure that the managerNamespace exists
	if !ki.namespaceExists(managerNamespace) {
		ns := &kates.Namespace{
			TypeMeta:   kates.TypeMeta{Kind: "Namespace"},
			ObjectMeta: kates.ObjectMeta{Name: managerNamespace},
		}
		dlog.Infof(c, "Creating namespace %q", managerNamespace)
		if err := ki.client.Create(c, ns, ns); err != nil {
			// trap race condition. If it's there, then all is good.
			if !errors2.IsAlreadyExists(err) {
				return nil, err
			}
		}
	}

	dlog.Infof(c, "Installing traffic-manager service in namespace %s", managerNamespace)
	if err := ki.client.Create(c, svc, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

func (ki *installer) createManagerDeployment(c context.Context, env client.Env) error {
	dep := ki.managerDeployment(env)
	dlog.Infof(c, "Installing traffic-manager deployment in namespace %s. Image: %s", managerNamespace, managerImageName(env))
	return ki.client.Create(c, dep, dep)
}

// removeManager will remove the agent from all deployments listed in the given agents slice. Unless agentsOnly is true,
// it will also remove the traffic-manager service and deployment.
func (ki *installer) removeManagerAndAgents(c context.Context, agentsOnly bool, agents []*manager.AgentInfo) error {
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
			agent, err := ki.findDeployment(c, ai.Namespace, ai.Name)
			if err != nil {
				if !errors2.IsNotFound(err) {
					addError(err)
				}
				return
			}
			if err = ki.undoDeploymentMods(c, agent); err != nil {
				addError(err)
			}
		}()
	}
	// wait for all agents to be removed
	wg.Wait()

	if !agentsOnly && len(errs) == 0 {
		// agent removal succeeded. Remove the manager service and deployment
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := ki.removeManagerService(c); err != nil {
				addError(err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := ki.removeManagerDeployment(c); err != nil {
				addError(err)
			}
		}()
		wg.Wait()
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

func (ki *installer) removeManagerService(c context.Context) error {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: managerNamespace,
			Name:      managerAppName}}
	dlog.Infof(c, "Deleting traffic-manager service from namespace %s", managerNamespace)
	return ki.client.Delete(c, svc, svc)
}

func (ki *installer) removeManagerDeployment(c context.Context) error {
	dep := &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: managerNamespace,
			Name:      managerAppName,
		}}
	dlog.Infof(c, "Deleting traffic-manager deployment from namespace %s", managerNamespace)
	return ki.client.Delete(c, dep, dep)
}

func (ki *installer) updateDeployment(c context.Context, env client.Env, currentDep *kates.Deployment) (*kates.Deployment, error) {
	dep := ki.managerDeployment(env)
	dep.ResourceVersion = currentDep.ResourceVersion
	dlog.Infof(c, "Updating traffic-manager deployment in namespace %s. Image: %s", managerNamespace, managerImageName(env))
	err := ki.client.Update(c, dep, dep)
	if err != nil {
		return nil, err
	}
	return dep, err
}

func svcPortByName(svc *kates.Service, name string) []*kates.ServicePort {
	svcPorts := make([]*kates.ServicePort, 0)
	ports := svc.Spec.Ports
	for i := range ports {
		port := &ports[i]
		if name == "" || name == port.Name {
			svcPorts = append(svcPorts, port)
		}
	}
	return svcPorts
}

func (ki *installer) findMatchingServices(portName string, dep *kates.Deployment) []*kates.Service {
	labels := dep.Spec.Template.Labels
	matching := make([]*kates.Service, 0)

	ki.accLock.Lock()
	for _, watch := range ki.watchers {
	nextSvc:
		for _, svc := range watch.Services {
			selector := svc.Spec.Selector
			if len(selector) == 0 {
				continue nextSvc
			}
			for k, v := range selector {
				if labels[k] != v {
					continue nextSvc
				}
			}
			if len(svcPortByName(svc, portName)) > 0 {
				matching = append(matching, svc)
			}
		}
	}
	ki.accLock.Unlock()
	return matching
}

func findMatchingPort(dep *kates.Deployment, portName string, svcs []*kates.Service) (
	service *kates.Service,
	sPort *kates.ServicePort,
	cn *kates.Container,
	cPortIndex int,
	err error,
) {
	// Sort slice of services so that the ones in the same namespace get prioritized.
	sort.Slice(svcs, func(i, j int) bool {
		a := svcs[i]
		b := svcs[j]
		if a.Namespace != b.Namespace {
			if a.Namespace == dep.Namespace {
				return true
			}
			if b.Namespace == dep.Namespace {
				return false
			}
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	cns := dep.Spec.Template.Spec.Containers
	for _, svc := range svcs {
		// For now, we only support intercepting one port on a given service.
		ports := svcPortByName(svc, portName)
		if len(ports) > 1 {
			return nil, nil, nil, 0, fmt.Errorf(
				"found matching service with multiple ports for deployment %s. Please specify the service port you want to intercept like so --port local:svcPortName", dep.Name)
		}
		port := ports[0]
		var msp *corev1.ServicePort
		var ccn *corev1.Container
		var cpi int

		if port.TargetPort.Type == intstr.String {
			portName := port.TargetPort.StrVal
			for ci := 0; ci < len(cns) && ccn == nil; ci++ {
				cn := &cns[ci]
				for pi := range cn.Ports {
					if cn.Ports[pi].Name == portName {
						msp = port
						ccn = cn
						cpi = pi
						break
					}
				}
			}
		} else {
			portNum := port.TargetPort.IntVal
			// Here we are using cpi <=0 instead of ccn == nil because if a
			// container has no ports, we want to use it but we don't want
			// to break out of the loop looking at containers in case there
			// is a better fit.  Currently, that is a container where the
			// ContainerPort matches the targetPort in the service.
			for ci := 0; ci < len(cns) && cpi <= 0; ci++ {
				cn := &cns[ci]
				if len(cn.Ports) == 0 {
					msp = port
					ccn = cn
					cpi = -1
				}
				for pi := range cn.Ports {
					if cn.Ports[pi].ContainerPort == portNum {
						msp = port
						ccn = cn
						cpi = pi
						break
					}
				}
			}
		}

		switch {
		case msp == nil:
			continue
		case sPort == nil:
			service = svc
			sPort = msp
			cPortIndex = cpi
			cn = ccn
		case sPort.TargetPort == msp.TargetPort:
			// Keep the chosen one
		case sPort.TargetPort.Type == intstr.String && msp.TargetPort.Type == intstr.Int:
			// Keep the chosen one
		case sPort.TargetPort.Type == intstr.Int && msp.TargetPort.Type == intstr.String:
			// Prefer targetPort in string format
			service = svc
			sPort = msp
			cPortIndex = cpi
			cn = ccn
		default:
			// Conflict
			return nil, nil, nil, 0, fmt.Errorf(
				"found services with conflicting port mappings to deployment %s. Please use --service to specify", dep.Name)
		}
	}

	if sPort == nil {
		return nil, nil, nil, 0, fmt.Errorf("found no services with a port that matches a container in deployment %s", dep.Name)
	}
	return service, sPort, cn, cPortIndex, nil
}

var agentNotFound = errors.New("no such agent")

// Determines if the port associated with a past-intercepted deployment has
// changed
func portChanged(c context.Context, dep *kates.Deployment, portName string) (bool, error) {
	var actions deploymentActions
	annotationsFound, err := getAnnotation(dep, &actions)
	if err != nil {
		return false, err
	}
	if annotationsFound {
		// If the portName in the annotation doesn't match the portName passed in
		// then the ports have changed.
		curSvcPort := actions.ReferencedServicePortName
		if curSvcPort != portName {
			dlog.Infof(c, "Port for %s changed from %s -> %s", dep.Name, curSvcPort, portName)
			return true, nil
		}
	}
	return false, nil
}

func (ki *installer) ensureAgent(c context.Context, namespace, name, portName, agentImageName string) error {
	dep, err := ki.findDeployment(c, namespace, name)
	if err != nil {
		if errors2.IsNotFound(err) {
			err = agentNotFound
		}
		return err
	}
	var agentContainer *kates.Container
	for i := range dep.Spec.Template.Spec.Containers {
		container := &dep.Spec.Template.Spec.Containers[i]
		if container.Name == agentContainerName {
			agentContainer = container
			break
		}
	}

	svcPortChanged, err := portChanged(c, dep, portName)
	if err != nil {
		return err
	}
	var svc *kates.Service

	switch {
	case svcPortChanged:
		dlog.Infof(c, "Port to be intercepted changed for deployment %s.%s", name, namespace)
		dlog.Info(c, "Undoing dep+svc modifications and removing agent")
		// Remove deployment+svc modifications, as well as traffic-agent since
		// the port is vital in configuring all of those
		if err = ki.undoDeploymentMods(c, dep); err != nil {
			return err
		}

		if err = ki.waitForApply(c, namespace, name, dep); err != nil {
			return err
		}
		// Since the agent has been removed, we find the updated version of
		// the deployment and fallthrough to the no agent case
		dep, err = ki.findDeployment(c, namespace, name)
		if err != nil {
			return nil
		}
		fallthrough
	case agentContainer == nil:
		dlog.Infof(c, "no agent found for deployment %s.%s", name, namespace)
		dlog.Infof(c, "Using port name %q", portName)
		matchingSvcs := ki.findMatchingServices(portName, dep)
		if len(matchingSvcs) == 0 {
			errMsg := fmt.Sprintf("Found no services with a selector matching labels %v", dep.Spec.Template.Labels)
			if portName != "" {
				errMsg += fmt.Sprintf(" and a port named %s", portName)
			}
			return errors.New(errMsg)
		}
		var err error
		dep, svc, err = addAgentToDeployment(c, portName, agentImageName, dep, matchingSvcs)
		if err != nil {
			return err
		}
	case agentContainer.Image != agentImageName:
		var actions deploymentActions
		ok, err := getAnnotation(dep, &actions)
		if err != nil {
			return err
		} else if !ok {
			// This can only happen if someone manually tampered with the annTelepresenceActions annotation
			return fmt.Errorf("expected %q annotation not found in %s.%s", annTelepresenceActions, name, namespace)
		}

		dlog.Debugf(c, "Updating agent for deployment %s.%s", name, namespace)
		aaa := actions.AddTrafficAgent
		explainUndo(c, aaa, dep)
		aaa.ImageName = agentImageName
		agentContainer.Image = agentImageName
		explainDo(c, aaa, dep)
	default:
		dlog.Debugf(c, "Deployment %s.%s already has an installed and up-to-date agent", name, namespace)
	}

	if err := ki.client.Update(c, dep, dep); err != nil {
		return err
	}
	if svc != nil {
		if err := ki.client.Update(c, svc, svc); err != nil {
			return err
		}
	}
	return ki.waitForApply(c, namespace, name, dep)
}

func (ki *installer) waitForApply(c context.Context, namespace, name string, dep *kates.Deployment) error {
	c, cancel := context.WithTimeout(c, 1*time.Minute)
	defer cancel()

	origGeneration := int64(0)
	if dep != nil {
		origGeneration = dep.ObjectMeta.Generation
	}

	timeout := fmt.Errorf("timeout while waiting for install/update of %s.%s", name, namespace)
	for {
		dtime.SleepWithContext(c, time.Second)
		if c.Err() != nil {
			return timeout
		}

		dep, err := ki.findDeployment(c, namespace, name)
		if err != nil {
			if c.Err() != nil {
				err = timeout
			}
			return err
		}

		deployed := dep.ObjectMeta.Generation >= origGeneration &&
			dep.Status.ObservedGeneration == dep.ObjectMeta.Generation &&
			(dep.Spec.Replicas == nil || dep.Status.UpdatedReplicas >= *dep.Spec.Replicas) &&
			dep.Status.UpdatedReplicas == dep.Status.Replicas &&
			dep.Status.AvailableReplicas == dep.Status.Replicas

		if deployed {
			dlog.Debugf(c, "deployment %s.%s successfully applied", name, namespace)
			return nil
		}
	}
}

func getAnnotation(obj kates.Object, data interface{}) (bool, error) {
	ann := obj.GetAnnotations()
	if ann == nil {
		return false, nil
	}
	ajs, ok := ann[annTelepresenceActions]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal([]byte(ajs), data); err != nil {
		return false, err
	}

	annV, err := semver.Parse(data.(multiAction).version())
	if err != nil {
		return false, fmt.Errorf("unable to parse semantic version in annotation %s of %s %s", annTelepresenceActions,
			obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
	}
	ourV := client.Semver()

	// Compare major and minor versions. 100% backward compatibility is assumed and greater patch versions are allowed
	if ourV.Major < annV.Major || ourV.Major == annV.Major && ourV.Minor < annV.Minor {
		return false, fmt.Errorf("the version %v found in annotation %s of %s %s is more recent than version %v of this binary",
			annV, annTelepresenceActions, obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), ourV)
	}
	return true, nil
}

func (ki *installer) undoDeploymentMods(c context.Context, dep *kates.Deployment) error {
	var actions deploymentActions
	ok, err := getAnnotation(dep, &actions)
	if !ok {
		return err
	}

	if svc := ki.findSvc(dep.Namespace, actions.ReferencedService); svc != nil {
		if err = ki.undoServiceMods(c, svc); err != nil {
			return err
		}
	}

	if err = actions.undo(dep); err != nil {
		return err
	}
	delete(dep.Annotations, annTelepresenceActions)
	explainUndo(c, &actions, dep)
	return ki.client.Update(c, dep, dep)
}

func (ki *installer) undoServiceMods(c context.Context, svc *kates.Service) error {
	var actions svcActions
	ok, err := getAnnotation(svc, &actions)
	if !ok {
		return err
	}
	if err = actions.undo(svc); err != nil {
		return err
	}
	delete(svc.Annotations, annTelepresenceActions)
	explainUndo(c, &actions, svc)
	return ki.client.Update(c, svc, svc)
}

func addAgentToDeployment(
	c context.Context,
	portName string,
	agentImageName string,
	deployment *kates.Deployment, matchingServices []*kates.Service,
) (
	*kates.Deployment,
	*kates.Service,
	error,
) {
	service, servicePort, container, containerPortIndex, err := findMatchingPort(deployment, portName, matchingServices)
	if err != nil {
		return nil, nil, err
	}
	dlog.Debugf(c, "using service %q port %q when intercepting deployment %q",
		service.Name,
		func() string {
			if servicePort.Name != "" {
				return servicePort.Name
			}
			return strconv.Itoa(int(servicePort.Port))
		}(),
		deployment.Name)

	version := client.Semver().String()

	// Try to detect the container port we'll be taking over.
	var containerPort struct {
		Name     string // If the existing container port doesn't have a name, we'll make one up.
		Number   uint16
		Protocol corev1.Protocol
	}
	// Start by filling from the servicePort; if these are the zero values, that's OK.
	containerPort.Name = servicePort.TargetPort.StrVal
	containerPort.Number = uint16(servicePort.TargetPort.IntVal)
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
		return nil, nil, fmt.Errorf("unable to add agent to deployment %s. The container port cannot be determined", deployment.Name)
	}
	if containerPort.Name == "" {
		containerPort.Name = fmt.Sprintf("tel2px-%d", containerPort.Number)
	}

	// Figure what modifications we need to make.
	deploymentMod := &deploymentActions{
		Version:                   version,
		ReferencedService:         service.Name,
		ReferencedServicePortName: portName,
		AddTrafficAgent: &addTrafficAgentAction{
			containerName:       container.Name,
			ContainerPortName:   containerPort.Name,
			ContainerPortProto:  containerPort.Protocol,
			ContainerPortNumber: containerPort.Number,
			ImageName:           agentImageName,
		},
	}
	// Depending on whether the Service refers to the port by name or by number, we either need
	// to patch the names in the deployment, or the number in the service.
	var serviceMod *svcActions
	if servicePort.TargetPort.Type == intstr.Int {
		// Change the port number that the Service refers to.
		serviceMod = &svcActions{
			Version: version,
			MakePortSymbolic: &makePortSymbolicAction{
				PortName:     servicePort.Name,
				TargetPort:   containerPort.Number,
				SymbolicName: containerPort.Name,
			},
		}
		// Since we are updating the service to use the containerPort.Name
		// if that value came from the container, then we need to hide it
		// since the service is using the targetPort's int.
		if usedContainerName {
			deploymentMod.HideContainerPort = &hideContainerPortAction{
				ContainerName: container.Name,
				PortName:      containerPort.Name,
			}
		}
	} else {
		// Hijack the port name in the Deployment.
		deploymentMod.HideContainerPort = &hideContainerPortAction{
			ContainerName: container.Name,
			PortName:      containerPort.Name,
		}
	}

	// Apply the actions on the Deployment.
	if err = deploymentMod.do(deployment); err != nil {
		return nil, nil, err
	}
	if deployment.Annotations == nil {
		deployment.Annotations = make(map[string]string)
	}
	deployment.Annotations[annTelepresenceActions] = deploymentMod.String()
	explainDo(c, deploymentMod, deployment)

	// Apply the actions on the Service.
	if serviceMod != nil {
		if err = serviceMod.do(service); err != nil {
			return nil, nil, err
		}
		if service.Annotations == nil {
			service.Annotations = make(map[string]string)
		}
		service.Annotations[annTelepresenceActions] = serviceMod.String()
		explainDo(c, serviceMod, service)
	} else {
		service = nil
	}

	return deployment, service, nil
}

func (ki *installer) managerDeployment(env client.Env) *kates.Deployment {
	replicas := int32(1)

	var containerEnv []corev1.EnvVar

	containerEnv = append(containerEnv, corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"})
	if env.SystemAHost != "" {
		containerEnv = append(containerEnv, corev1.EnvVar{Name: "SYSTEMA_HOST", Value: env.SystemAHost})
	}
	if env.SystemAPort != "" {
		containerEnv = append(containerEnv, corev1.EnvVar{Name: "SYSTEMA_PORT", Value: env.SystemAPort})
	}

	return &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: managerNamespace,
			Name:      managerAppName,
			Labels:    labelMap,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labelMap,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelMap,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  managerAppName,
							Image: managerImageName(env),
							Env:   containerEnv,
							Ports: []corev1.ContainerPort{
								{
									Name:          "sshd",
									ContainerPort: ManagerPortSSH,
								},
								{
									Name:          "api",
									ContainerPort: ManagerPortHTTP,
								},
							}}},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
		},
	}
}

func (ki *installer) findManagerSvc(c context.Context) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta:   kates.TypeMeta{Kind: "Service"},
		ObjectMeta: kates.ObjectMeta{Name: managerAppName, Namespace: managerNamespace},
	}
	if err := ki.client.Get(c, svc, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

func (ki *installer) ensureManager(c context.Context, env client.Env) error {
	if _, err := ki.findManagerSvc(c); err != nil {
		if errors2.IsNotFound(err) {
			_, err = ki.createManagerSvc(c)
		}
		if err != nil {
			return err
		}
	}

	dep, err := ki.findDeployment(c, managerNamespace, managerAppName)
	if err != nil {
		if errors2.IsNotFound(err) {
			err = ki.createManagerDeployment(c, env)
			if err == nil {
				err = ki.waitForApply(c, managerNamespace, managerAppName, nil)
			}
		}
		return err
	}

	imageName := managerImageName(env)
	cns := dep.Spec.Template.Spec.Containers
	upToDate := false
	for i := range cns {
		cn := &cns[i]
		if cn.Image == imageName {
			upToDate = true
			break
		}
	}
	if upToDate {
		dlog.Infof(c, "%s.%s is up-to-date. Image: %s", managerAppName, managerNamespace, managerImageName(env))
	} else {
		_, err = ki.updateDeployment(c, env, dep)
		if err == nil {
			err = ki.waitForApply(c, managerNamespace, managerAppName, dep)
		}
	}
	return err
}
