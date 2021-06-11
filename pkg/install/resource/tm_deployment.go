package resource

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

const managerLicenseName = "systema-license"

type tmDeployment struct {
	found *kates.Deployment
}

func NewTrafficManagerDeployment() Instance {
	return &tmDeployment{}
}

func (ri *tmDeployment) deployment(ctx context.Context) *kates.Deployment {
	dep := new(kates.Deployment)
	dep.TypeMeta = kates.TypeMeta{
		Kind: "Deployment",
	}
	sc := getScope(ctx)
	dep.ObjectMeta = kates.ObjectMeta{
		Namespace: sc.namespace,
		Name:      install.ManagerAppName,
		Labels:    sc.tmSelector,
	}
	return dep
}

func (ri *tmDeployment) desiredDeployment(ctx context.Context) *kates.Deployment {
	replicas := int32(1)

	sc := getScope(ctx)
	var containerEnv = []corev1.EnvVar{
		{Name: "LOG_LEVEL", Value: "info"},
		{Name: "SYSTEMA_HOST", Value: sc.env.SystemAHost},
		{Name: "SYSTEMA_PORT", Value: sc.env.SystemAPort},
		{Name: "CLUSTER_ID", Value: sc.clusterID},
		{Name: "TELEPRESENCE_REGISTRY", Value: sc.env.Registry},

		// Manager needs to know its own namespace so that it can propagate that when
		// to agents when injecting them
		{
			Name: "MANAGER_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
	}
	if sc.env.AgentImage != "" {
		containerEnv = append(containerEnv, corev1.EnvVar{Name: "TELEPRESENCE_AGENT_IMAGE", Value: sc.env.AgentImage})
	}

	optional := true
	volumes := []corev1.Volume{
		{
			Name: "license",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: managerLicenseName,
					Optional:   &optional,
				},
			},
		},
		{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: install.MutatorWebhookTLSName,
					Optional:   &optional,
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "license",
			ReadOnly:  true,
			MountPath: "/home/telepresence/",
		},
		{
			Name:      "tls",
			ReadOnly:  true,
			MountPath: "/var/run/secrets/tls",
		},
	}

	dep := ri.deployment(ctx)
	dep.Spec = appsv1.DeploymentSpec{
		Replicas: &replicas,
		Selector: &metav1.LabelSelector{
			MatchLabels: sc.tmSelector,
		},
		Template: kates.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: sc.tmSelector,
			},
			Spec: corev1.PodSpec{
				Volumes: volumes,
				Containers: []corev1.Container{
					{
						Name:  install.ManagerAppName,
						Image: ri.imageName(ctx),
						Env:   containerEnv,
						Ports: []corev1.ContainerPort{
							{
								Name:          "api",
								ContainerPort: install.ManagerPortHTTP,
							},
							{
								Name:          "https",
								ContainerPort: install.MutatorWebhookPortHTTPS,
							},
						},
						VolumeMounts: volumeMounts,
					}},
				ServiceAccountName: install.ManagerAppName,
			},
		},
	}
	return dep
}

func (ri *tmDeployment) imageName(ctx context.Context) string {
	return fmt.Sprintf("%s/tel2:%s", getScope(ctx).env.Registry, strings.TrimPrefix(client.Version(), "v"))
}

func (ri *tmDeployment) Create(ctx context.Context) error {
	return create(ctx, ri.desiredDeployment(ctx))
}

func (ri *tmDeployment) Exists(ctx context.Context) (bool, error) {
	found, err := find(ctx, ri.deployment(ctx))
	if err != nil {
		return false, err
	}
	if found == nil {
		return false, nil
	}
	ri.found = found.(*kates.Deployment)
	return true, nil
}

func (ri *tmDeployment) Delete(ctx context.Context) error {
	return remove(ctx, ri.deployment(ctx))
}

func (ri *tmDeployment) Update(ctx context.Context) error {
	if ri.found == nil {
		return nil
	}

	imageName := ri.imageName(ctx)
	currentPodSpec := &ri.found.Spec.Template.Spec
	cns := currentPodSpec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Image == imageName {
			dlog.Infof(ctx, "%s is up-to-date. Image: %s", logName(ri.found), imageName)
			return nil
		}
	}

	dep := ri.desiredDeployment(ctx)
	dep.ResourceVersion = ri.found.ResourceVersion
	dlog.Infof(ctx, "Updating %s. Image: %s", logName(dep), imageName)
	if err := getScope(ctx).client.Update(ctx, dep, dep); err != nil {
		return fmt.Errorf("failed to update %s: %w", logName(dep), err)
	}
	return waitForDeployApply(ctx, dep)
}
