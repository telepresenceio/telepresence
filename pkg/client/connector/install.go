package connector

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/datawire/telepresence2/pkg/client"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type installer struct {
	client    *kates.Client
	namespace string
}

func newTrafficManagerInstaller(kubeconfig, context, namespace string) (*installer, error) {
	client, err := kates.NewClient(kates.ClientOptions{
		Kubeconfig: kubeconfig,
		Context:    context,
		Namespace:  namespace})
	if err != nil {
		return nil, err
	}
	return &installer{client: client, namespace: namespace}, nil
}

const sshdPort = 8022
const apiPort = 8081
const appName = "traffic-manager"
const telName = "manager"

var labelMap = map[string]string{
	"app":          appName,
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

func (ki *installer) findDeployment(p *supervisor.Process, name string) (*kates.Deployment, error) {
	dep := &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.namespace,
			Name:      name},
	}
	if err := ki.client.Get(p.Context(), dep, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

func (ki *installer) findSvc(p *supervisor.Process, name string) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.namespace,
			Name:      name},
	}
	if err := ki.client.Get(p.Context(), svc, svc); err != nil {
		return nil, err
	}
	p.Logf("Found existing traffic-manager service in namespace %s", ki.namespace)
	return svc, nil
}

func (ki *installer) createManagerSvc(p *supervisor.Process) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.namespace,
			Name:      appName},
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

	p.Logf("Installing traffic-manager service in namespace %s", ki.namespace)
	if err := ki.client.Create(p.Context(), svc, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

func (ki *installer) createManagerDeployment(p *supervisor.Process) error {
	dep := ki.depManifest()
	p.Logf("Installing traffic-manager deployment in namespace %s. Image: %s", ki.namespace, managerImageName())
	return ki.client.Create(p.Context(), dep, dep)
}

func (ki *installer) updateDeployment(p *supervisor.Process, currentDep *kates.Deployment) (*kates.Deployment, error) {
	dep := ki.depManifest()
	dep.ResourceVersion = currentDep.ResourceVersion
	p.Logf("Updating traffic-manager deployment in namespace %s. Image: %s", ki.namespace, managerImageName())
	err := ki.client.Update(p.Context(), dep, dep)
	if err != nil {
		return nil, err
	}
	return dep, err
}

func (ki *installer) portToIntercept(p *supervisor.Process, name string, labels map[string]string, cns []corev1.Container) (
	service *corev1.Service, sPort *corev1.ServicePort, cn *corev1.Container, cPortIndex int, err error) {
	svcs := make([]*kates.Service, 0)
	err = ki.client.List(p.Context(), kates.Query{
		Name:      name,
		Kind:      "svc",
		Namespace: ki.namespace,
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
	return findMatchingPort(p, matching, cns)
}

func findMatchingPort(p *supervisor.Process, svcs []*kates.Service, cns []corev1.Container) (
	service *corev1.Service, sPort *corev1.ServicePort, cn *corev1.Container, cPortIndex int, err error) {
	for _, svc := range svcs {
		ports := svc.Spec.Ports
		if len(ports) != 1 {
			// TODO: Propagate warning about this to the user
			p.Logf("discarding service %s because it has %d number of ports", svc.Name, len(ports))
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
				for pi := range cns[ci].Ports {
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
				for pi := range cns[ci].Ports {
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
				"found services with conflicting port mappings to container %s. Please use --service to specify", cn.Name)
		}
	}

	if sPort == nil {
		return nil, nil, nil, 0, fmt.Errorf("found no services with a port that matches container %s", cn.Name)
	}
	// TODO: if sPort.TargetType.Type == intstr.Int, then the svc must be updated to use a named port

	return service, sPort, cn, cPortIndex, nil
}

func (ki *installer) ensureAgent(p *supervisor.Process, name, svcName string) error {
	dep, err := ki.findDeployment(p, name)
	if err != nil {
		if kates.IsNotFound(err) {
			err = fmt.Errorf("no such deployment %q", name)
		}
		return err
	}
	cns := dep.Spec.Template.Spec.Containers
	for i := len(cns) - 1; i >= 0; i-- {
		if cns[i].Name == "traffic-agent" {
			p.Logf("deployment %q already has an agent", name)
			return nil
		}
	}
	return ki.addAgentToDeployment(p, svcName, dep)
}

func (ki *installer) addAgentToDeployment(p *supervisor.Process, svcName string, dep *kates.Deployment) error {
	tplSpec := &dep.Spec.Template.Spec
	containers := tplSpec.Containers
	if len(containers) != 1 {
		// TODO: How do we handle multiple containers?
		return fmt.Errorf("unable to add agent to deployment %s. It doesn't have one container", dep.Name)
	}
	svc, sPort, icn, cPortIndex, err := ki.portToIntercept(p, svcName, dep.Spec.Template.Labels, containers)
	if err != nil {
		return err
	}
	p.Logf("using service %s port %s when intercepting deployment %q", svc.Name, sPort.Name, dep.Name)

	svcNeedsUpdate := false
	if sPort.TargetPort.Type == intstr.Int {
		// Service needs to use a named port
		sPort.TargetPort = intstr.FromString("tele-proxied")
		svcNeedsUpdate = true
	}

	// Remove name and change container port of the port appointed by the service
	icp := &icn.Ports[cPortIndex]
	icp.Name = ""

	tplSpec.Containers = []corev1.Container{*icn, {
		Name:  "traffic-agent",
		Image: managerImageName(),
		Args:  []string{"agent"},
		Ports: []corev1.ContainerPort{{
			Name:          sPort.TargetPort.StrVal,
			ContainerPort: 9900,
		}},
		Env: []corev1.EnvVar{{
			Name:  "LOG_LEVEL",
			Value: "debug",
		}, {
			Name:  "AGENT_NAME",
			Value: dep.Name,
		}, {
			Name:  "APP_PORT",
			Value: strconv.Itoa(int(icp.ContainerPort)),
		}}}}

	p.Logf("Adding agent to deployment %s in namespace %s. Image: %s", dep.Name, ki.namespace, managerImageName())
	if err = ki.client.Update(p.Context(), dep, dep); err != nil {
		return err
	}
	if svcNeedsUpdate {
		if err = ki.client.Update(p.Context(), svc, svc); err != nil {
			return err
		}
	}
	return nil
}

func (ki *installer) depManifest() *kates.Deployment {
	replicas := int32(1)
	return &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: ki.namespace,
			Name:      appName,
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
							Name:  appName,
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

func (ki *installer) ensureManager(p *supervisor.Process) (int32, int32, error) {
	svc, err := ki.findSvc(p, appName)
	if err != nil {
		if !kates.IsNotFound(err) {
			return 0, 0, err
		}
		svc, err = ki.createManagerSvc(p)
		if err != nil {
			return 0, 0, err
		}
	}
	dep, err := ki.findDeployment(p, appName)
	if err != nil {
		if !kates.IsNotFound(err) {
			return 0, 0, err
		}
		err = ki.createManagerDeployment(p)
	} else {
		_, err = ki.updateDeployment(p, dep)
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
