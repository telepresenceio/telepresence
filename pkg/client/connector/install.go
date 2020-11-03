package connector

import (
	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type tmInstaller struct {
	client *kates.Client
}

func newTrafficManagerInstaller(kubeconfig, context string) (*tmInstaller, error) {
	client, err := kates.NewClient(kates.ClientOptions{
		Kubeconfig: kubeconfig,
		Context:    context})
	if err != nil {
		return nil, err
	}
	return &tmInstaller{client: client}, nil
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
var ManagerImage string

func (ki *tmInstaller) findDeployment(p *supervisor.Process, namespace string) (*kates.Deployment, error) {
	dep := &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: namespace,
			Name:      appName},
	}
	if err := ki.client.Get(p.Context(), dep, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

func (ki *tmInstaller) findSvc(p *supervisor.Process, namespace string) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: namespace,
			Name:      appName},
	}
	if err := ki.client.Get(p.Context(), svc, svc); err != nil {
		return nil, err
	}
	p.Logf("Found existing traffic-manager service in namespace %s", namespace)
	return svc, nil
}

func (ki *tmInstaller) installSvc(p *supervisor.Process, namespace string) (*kates.Service, error) {
	svc := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: namespace,
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

	p.Logf("Installing traffic-manager service in namespace %s", namespace)
	if err := ki.client.Create(p.Context(), svc, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

func (ki *tmInstaller) createDeployment(p *supervisor.Process, namespace string) (*kates.Deployment, error) {
	dep := ki.depManifest(namespace)
	p.Logf("Installing traffic-manager deployment in namespace %s", namespace)
	err := ki.client.Create(p.Context(), dep, dep)
	if err != nil {
		return nil, err
	}
	return dep, err
}

func (ki *tmInstaller) updateDeployment(p *supervisor.Process, namespace string, currentDep *kates.Deployment) (*kates.Deployment, error) {
	dep := ki.depManifest(namespace)
	dep.ResourceVersion = currentDep.ResourceVersion
	p.Logf("Updating traffic-manager deployment in namespace %s", namespace)
	err := ki.client.Update(p.Context(), dep, dep)
	if err != nil {
		return nil, err
	}
	return dep, err
}

func (ki *tmInstaller) depManifest(namespace string) *kates.Deployment {
	replicas := int32(1)
	return &kates.Deployment{
		TypeMeta: kates.TypeMeta{
			Kind: "Deployment",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: namespace,
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
							Image: ManagerImage,
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

func (ki *tmInstaller) ensure(p *supervisor.Process, namespace string) (int32, int32, error) {
	svc, err := ki.findSvc(p, namespace)
	if err != nil {
		if !kates.IsNotFound(err) {
			return 0, 0, err
		}
		svc, err = ki.installSvc(p, namespace)
		if err != nil {
			return 0, 0, err
		}
	}
	dep, err := ki.findDeployment(p, namespace)
	if err != nil {
		if !kates.IsNotFound(err) {
			return 0, 0, err
		}
		_, err = ki.createDeployment(p, namespace)
	} else {
		_, err = ki.updateDeployment(p, namespace, dep)
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
