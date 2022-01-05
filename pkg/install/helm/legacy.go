package helm

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/go-multierror"
	admreg "k8s.io/api/admissionregistration/v1"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

func getLegacyObjects(namespace string) []kates.Object {
	selector := map[string]string{
		"app":          install.ManagerAppName,
		"telepresence": "manager",
	}
	return []kates.Object{
		&kates.ServiceAccount{
			TypeMeta: kates.TypeMeta{
				Kind:       "ServiceAccount",
				APIVersion: "v1",
			},
			ObjectMeta: kates.ObjectMeta{
				Namespace: namespace,
				Name:      install.ManagerAppName,
			},
		},
		&kates.ClusterRole{
			TypeMeta: kates.TypeMeta{
				Kind:       "ClusterRole",
				APIVersion: "rbac.authorization.k8s.io/v1",
			},
			ObjectMeta: kates.ObjectMeta{
				Name: fmt.Sprintf("%s-%s", install.ManagerAppName, namespace),
			},
		},
		&kates.ClusterRoleBinding{
			TypeMeta: kates.TypeMeta{
				Kind:       "ClusterRoleBinding",
				APIVersion: "rbac.authorization.k8s.io/v1",
			},
			ObjectMeta: kates.ObjectMeta{
				Name: fmt.Sprintf("%s-%s", install.ManagerAppName, namespace),
			},
		},
		&kates.Role{
			TypeMeta: kates.TypeMeta{
				Kind:       "Role",
				APIVersion: "rbac.authorization.k8s.io/v1",
			},
			ObjectMeta: kates.ObjectMeta{
				Namespace: namespace,
				Name:      install.ManagerAppName,
			},
		},
		&kates.RoleBinding{
			TypeMeta: kates.TypeMeta{
				Kind:       "RoleBinding",
				APIVersion: "rbac.authorization.k8s.io/v1",
			},
			ObjectMeta: kates.ObjectMeta{
				Namespace: namespace,
				Name:      install.ManagerAppName,
			},
		},
		&kates.Secret{
			TypeMeta: kates.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: kates.ObjectMeta{
				Namespace: namespace,
				Name:      install.MutatorWebhookTLSName,
			},
		},
		&kates.Service{
			TypeMeta: kates.TypeMeta{
				Kind: "Service",
			},
			ObjectMeta: kates.ObjectMeta{
				Namespace: namespace,
				Name:      install.ManagerAppName,
			},
		},
		&kates.Service{
			TypeMeta: kates.TypeMeta{
				Kind: "Service",
			},
			ObjectMeta: kates.ObjectMeta{
				Namespace: namespace,
				Name:      install.AgentInjectorName,
			},
		},
		&admreg.MutatingWebhookConfiguration{
			TypeMeta: kates.TypeMeta{
				Kind:       "MutatingWebhookConfiguration",
				APIVersion: "admissionregistration.k8s.io/v1",
			},
			ObjectMeta: kates.ObjectMeta{
				Name: fmt.Sprintf("%s-webhook-%s", install.AgentInjectorName, namespace),
			},
		},
		&kates.Deployment{
			TypeMeta: kates.TypeMeta{
				Kind: "Deployment",
			},
			ObjectMeta: kates.ObjectMeta{
				Namespace: namespace,
				Name:      install.ManagerAppName,
				Labels:    selector,
			},
		},
	}
}

func importObject(ctx context.Context, obj kates.Object, namespace string, client *kates.Client) error {
	into := obj.DeepCopyObject().(kates.Object)
	if err := client.Get(ctx, obj, into); err != nil {
		// If the object isn't there we're not worried: it'll be created by the helm chart if necessary
		if !kates.IsNotFound(err) {
			return fmt.Errorf("error getting resource %s/%s: %w", obj.GetObjectKind(), obj.GetName(), err)
		}
		return nil
	}
	labels := into.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app.kubernetes.io/managed-by"] = "Helm"
	into.SetLabels(labels)
	annotations := into.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	// Prevent us from taking over an existing release
	// This is really done out of an abundance of caution, as EnsureTrafficManager should validate that there is no existing
	// release before calling importLegacy
	if release, ok := annotations["meta.helm.sh/release-name"]; ok && release != releaseName {
		return fmt.Errorf("refusing to replace existing release annotation %s in object %s/%s", release, obj.GetObjectKind(), obj.GetName())
	}
	if ns, ok := annotations["meta.helm.sh/release-namespace"]; ok && ns != namespace {
		return fmt.Errorf("refusing to replace existing namespace annotation %s in object %s/%s", ns, obj.GetObjectKind(), obj.GetName())
	}
	annotations["meta.helm.sh/release-name"] = releaseName
	annotations["meta.helm.sh/release-namespace"] = namespace
	into.SetAnnotations(annotations)
	if err := client.Update(ctx, into, nil); err != nil {
		return fmt.Errorf("error updating resource %s/%s: %w", obj.GetObjectKind(), obj.GetName(), err)
	}
	return nil
}

func importLegacy(ctx context.Context, namespace string, client *kates.Client) error {
	objects := getLegacyObjects(namespace)
	wg := sync.WaitGroup{}
	wg.Add(len(objects))
	errors := make(chan (error), len(objects))
	for _, o := range objects {
		obj := o
		go func() {
			defer wg.Done()
			if err := importObject(ctx, obj, namespace, client); err != nil {
				errors <- err
			}
		}()
	}
	wg.Wait()
	close(errors)
	var result error
	for err := range errors {
		dlog.Error(ctx, err)
		result = multierror.Append(result, err)
	}
	return result
}
