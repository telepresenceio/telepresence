package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/blang/semver"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/rpc/manager"
)

type installer struct {
	*k8sCluster
}

func newTrafficManagerInstaller(kc *k8sCluster) (*installer, error) {
	return &installer{k8sCluster: kc}, nil
}

const sshdPort = 8022
const apiPort = 8081
const managerAppName = "traffic-manager"
const telName = "manager"
const domainPrefix = "telepresence.datawire.io/"
const annTelepresenceActions = domainPrefix + "actions"

var labelMap = map[string]string{
	"app":          managerAppName,
	"telepresence": telName,
}

// ManagerImage is inserted at build using --ldflags -X
var managerImage string

var resolveManagerName = sync.Once{}

func managerImageName() string {
	resolveManagerName.Do(func() {
		dockerReg := os.Getenv("TELEPRESENCE_REGISTRY")
		if dockerReg == "" {
			dockerReg = "docker.io/datawire"
		}
		managerImage = fmt.Sprintf("%s/tel2:%s", dockerReg, client.Version())
	})
	return managerImage
}

func (ki *installer) findDeployment(c context.Context, name string) (*kates.Deployment, error) {
	dep := &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.Namespace,
			Name:      name},
	}
	if err := ki.client.Get(c, dep, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

func (ki *installer) findSvc(c context.Context, name string) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.Namespace,
			Name:      name},
	}
	if err := ki.client.Get(c, svc, svc); err != nil {
		return nil, err
	}
	dlog.Infof(c, "Found existing traffic-manager service in namespace %s", ki.Namespace)
	return svc, nil
}

func (ki *installer) createManagerSvc(c context.Context) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.Namespace,
			Name:      managerAppName},
		Spec: kates.ServiceSpec{
			Type:      "ClusterIP",
			ClusterIP: "None",
			Selector:  labelMap,
			Ports: []kates.ServicePort{
				{
					Name: "sshd",
					Port: sshdPort,
					TargetPort: kates.IntOrString{
						Type:   intstr.String,
						StrVal: "sshd",
					},
				},
				{
					Name: "api",
					Port: apiPort,
					TargetPort: kates.IntOrString{
						Type:   intstr.String,
						StrVal: "api",
					},
				},
			},
		},
	}

	dlog.Infof(c, "Installing traffic-manager service in namespace %s", ki.Namespace)
	if err := ki.client.Create(c, svc, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

func (ki *installer) createManagerDeployment(c context.Context) error {
	dep := ki.managerDeployment()
	dlog.Infof(c, "Installing traffic-manager deployment in namespace %s. Image: %s", ki.Namespace, managerImageName())
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
			agent, err := ki.findDeployment(c, ai.Name)
			if err != nil {
				if !kates.IsNotFound(err) {
					addError(err)
				}
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
			Namespace: ki.Namespace,
			Name:      managerAppName}}
	dlog.Infof(c, "Deleting traffic-manager service from namespace %s", ki.Namespace)
	return ki.client.Delete(c, svc, svc)
}

func (ki *installer) removeManagerDeployment(c context.Context) error {
	dep := &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.Namespace,
			Name:      managerAppName,
		}}
	dlog.Infof(c, "Deleting traffic-manager deployment from namespace %s", ki.Namespace)
	return ki.client.Delete(c, dep, dep)
}

func (ki *installer) updateDeployment(c context.Context, currentDep *kates.Deployment) (*kates.Deployment, error) {
	dep := ki.managerDeployment()
	dep.ResourceVersion = currentDep.ResourceVersion
	dlog.Infof(c, "Updating traffic-manager deployment in namespace %s. Image: %s", ki.Namespace, managerImageName())
	err := ki.client.Update(c, dep, dep)
	if err != nil {
		return nil, err
	}
	return dep, err
}

func (ki *installer) portToIntercept(c context.Context, name string, labels map[string]string, dep *kates.Deployment) (
	service *kates.Service, sPort *kates.ServicePort, cn *kates.Container, cPortIndex int, err error) {
	svcs := make([]*kates.Service, 0)
	err = ki.client.List(c, kates.Query{
		Name:      name,
		Kind:      "svc",
		Namespace: ki.Namespace,
	}, &svcs)
	if err != nil {
		return nil, nil, nil, 0, err
	}

	matching := make([]*kates.Service, 0)
	for _, svc := range svcs {
		selector := svc.Spec.Selector
		matchOk := len(selector) > 0
		for k, v := range selector {
			if labels[k] != v {
				matchOk = false
				break
			}
		}
		if matchOk {
			matching = append(matching, svc)
		}
	}

	if len(matching) == 0 {
		return nil, nil, nil, 0, fmt.Errorf("found no services with a selector matching labels %v", labels)
	}
	return findMatchingPort(c, dep, matching)
}

func findMatchingPort(c context.Context, dep *kates.Deployment, svcs []*kates.Service) (
	service *kates.Service, sPort *kates.ServicePort, cn *kates.Container, cPortIndex int, err error) {
	cns := dep.Spec.Template.Spec.Containers
	for _, svc := range svcs {
		ports := svc.Spec.Ports
		if len(ports) != 1 {
			// TODO: Propagate warning about this to the user
			dlog.Warnf(c, "discarding service %s because it has %d number of ports", svc.Name, len(ports))
			continue
		}
		port := &ports[0]
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
			for ci := 0; ci < len(cns) && ccn == nil; ci++ {
				cn := &cns[ci]
				if len(cn.Ports) == 0 {
					msp = port
					ccn = cn
					cpi = -1
					break
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

var agentExists = errors.New("agent exists")
var agentNotFound = errors.New("no such agent")

func (ki *installer) ensureAgent(c context.Context, name, svcName string) error {
	dep, err := ki.findDeployment(c, name)
	if err != nil {
		if kates.IsNotFound(err) {
			err = agentNotFound
		}
		return err
	}
	cns := dep.Spec.Template.Spec.Containers
	for i := len(cns) - 1; i >= 0; i-- {
		if cns[i].Name == "traffic-agent" {
			dlog.Debugf(c, "deployment %q already has an agent", name)
			return agentExists
		}
	}
	dlog.Infof(c, "no agent found for deployment %q", name)
	return ki.addAgentToDeployment(c, svcName, dep)
}

func getAnnotation(ann map[string]string, data interface{}) (bool, error) {
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

	annV, err := semver.Parse(data.(actions).version())
	if err != nil {
		return false, fmt.Errorf("unable to parse semantic version in annotation %s", annTelepresenceActions)
	}
	ourV := client.Semver()

	// Compare major and minor versions. 100% backward compatibility is assumed and greater patch versions are allowed
	if ourV.Major < annV.Major || ourV.Minor < annV.Minor {
		return false, fmt.Errorf("the version %v found in annotation %s is more recent than version %v of this binary",
			annTelepresenceActions, annV, ourV)
	}
	return true, nil
}

func (ki *installer) undoDeploymentMods(c context.Context, dep *kates.Deployment) error {
	var actions deploymentActions
	ok, err := getAnnotation(dep.Annotations, &actions)
	if !ok {
		return err
	}

	svc, err := ki.findSvc(c, actions.ReferencedService)
	if err != nil {
		if !kates.IsNotFound(err) {
			return fmt.Errorf("unable to get service %s when uninstalling agent in deployment %s: %v",
				actions.ReferencedService, dep.Name, err)
		}
	} else if err = ki.undoServiceMods(c, svc); err != nil {
		return err
	}

	if err = actions.undo(dep); err != nil {
		return err
	}
	delete(dep.Annotations, annTelepresenceActions)
	return ki.client.Update(c, dep, dep)
}

func (ki *installer) undoServiceMods(c context.Context, svc *kates.Service) error {
	var actions svcActions
	ok, err := getAnnotation(svc.Annotations, &actions)
	if !ok {
		return err
	}
	if err = actions.undo(svc); err != nil {
		return err
	}
	delete(svc.Annotations, annTelepresenceActions)
	return ki.client.Update(c, svc, svc)
}

func (ki *installer) addAgentToDeployment(c context.Context, svcName string, dep *kates.Deployment) error {
	tplSpec := &dep.Spec.Template.Spec
	containers := tplSpec.Containers
	if len(containers) != 1 {
		// TODO: How do we handle multiple containers?
		return fmt.Errorf("unable to add agent to deployment %s. It doesn't have one container", dep.Name)
	}
	svc, sPort, icn, cPortIndex, err := ki.portToIntercept(c, svcName, dep.Spec.Template.Labels, dep)
	if err != nil {
		return err
	}
	name := sPort.Name
	if name == "" {
		name = strconv.Itoa(int(sPort.Port))
	}
	dlog.Debugf(c, "using service %s port %s when intercepting deployment %q", svc.Name, name, dep.Name)

	targetPortSymbolic := true
	containerPort := -1
	version := client.Semver().String()

	if sPort.TargetPort.Type == intstr.Int {
		// Service needs to use a named port
		targetPortSymbolic = false
		containerPort = int(sPort.TargetPort.IntVal)
		svcActions := &svcActions{
			Version: version,
			MakePortSymbolic: &makePortSymbolicAction{
				PortName:   sPort.Name,
				TargetPort: containerPort,
			},
		}
		// apply the actions on the Service
		if err = svcActions.do(svc); err != nil {
			return err
		}

		// save the actions so that they can be undone.
		if svc.Annotations == nil {
			svc.Annotations = make(map[string]string)
		}
		svc.Annotations[annTelepresenceActions] = svcActions.String()
	}

	depActions := &deploymentActions{
		Version:           version,
		ReferencedService: svc.Name,
		AddTrafficAgent: &addTrafficAgentAction{
			ContainerPortName:  sPort.TargetPort.StrVal,
			ContainerPortProto: string(sPort.Protocol),
			AppPort:            containerPort,
		},
	}

	if cPortIndex >= 0 {
		// Remove name and change container port of the port appointed by the service
		icp := &icn.Ports[cPortIndex]
		containerPort = int(icp.ContainerPort)
		if targetPortSymbolic {
			depActions.HideContainerPort = &hideContainerPortAction{
				ContainerName: icn.Name,
				PortName:      sPort.TargetPort.StrVal,
			}
		}
	}

	if containerPort < 0 {
		return fmt.Errorf("unable to add agent to deployment %s. The container port cannot be determined", dep.Name)
	}

	// apply the actions on the Deployment
	if err = depActions.do(dep); err != nil {
		return err
	}

	// save the actions so that they can be undone.
	if dep.Annotations == nil {
		dep.Annotations = make(map[string]string)
	}
	dep.Annotations[annTelepresenceActions] = depActions.String()

	dlog.Infof(c, "Adding agent to deployment %s in namespace %s. Image: %s", dep.Name, ki.Namespace, managerImageName())
	if err = ki.client.Update(c, dep, dep); err != nil {
		return err
	}
	if !targetPortSymbolic {
		// service must be updated to use generated port name
		if err = ki.client.Update(c, svc, svc); err != nil {
			return err
		}
	}
	return nil
}

func (ki *installer) managerDeployment() *kates.Deployment {
	replicas := int32(1)
	return &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.Namespace,
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
							Image: managerImageName(),
							Env: []corev1.EnvVar{{
								Name:  "LOG_LEVEL",
								Value: "debug",
							}},
							Ports: []corev1.ContainerPort{
								{
									Name:          "sshd",
									ContainerPort: sshdPort,
								},
								{
									Name:          "api",
									ContainerPort: apiPort,
								},
							}}},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
		},
	}
}

func (ki *installer) ensureManager(c context.Context) (int32, int32, error) {
	svc, err := ki.findSvc(c, managerAppName)
	if err != nil {
		if !kates.IsNotFound(err) {
			return 0, 0, err
		}
		svc, err = ki.createManagerSvc(c)
		if err != nil {
			return 0, 0, err
		}
	}
	dep, err := ki.findDeployment(c, managerAppName)
	if err != nil {
		if !kates.IsNotFound(err) {
			return 0, 0, err
		}
		err = ki.createManagerDeployment(c)
	} else {
		_, err = ki.updateDeployment(c, dep)
	}
	if err != nil {
		return 0, 0, err
	}

	var sshdPort, apiPort int32
	for _, port := range svc.Spec.Ports {
		switch port.Name {
		case "sshd":
			sshdPort = port.Port
		case "api":
			apiPort = port.Port
		}
	}
	if sshdPort == 0 {
		return 0, 0, errors.New("traffic-manager has no sshd port configured")
	}
	if apiPort == 0 {
		return 0, 0, errors.New("traffic-manager has no api port configured")
	}
	return sshdPort, apiPort, nil
}
