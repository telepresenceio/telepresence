package helm

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/go-multierror"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func getLegacyFuncs(ctx context.Context, namespace string) []func() error {
	selector := map[string]string{
		"app":          install.ManagerAppName,
		"telepresence": "manager",
	}
	getOpts := meta.GetOptions{}
	updOpts := meta.UpdateOptions{}
	ki := k8sapi.GetK8sInterface(ctx)
	return []func() error{
		func() error {
			kif := ki.CoreV1().ServiceAccounts(namespace)
			o, err := kif.Get(ctx, install.ManagerAppName, getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "ServiceAccount", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.RbacV1().ClusterRoles()
			o, err := kif.Get(ctx, fmt.Sprintf("%s-%s", install.ManagerAppName, namespace), getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "ClusterRole", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.RbacV1().ClusterRoleBindings()
			o, err := kif.Get(ctx, fmt.Sprintf("%s-%s", install.ManagerAppName, namespace), getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "ClusterRoleBinding", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.RbacV1().Roles(namespace)
			o, err := kif.Get(ctx, install.ManagerAppName, getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "Role", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.RbacV1().RoleBindings(namespace)
			o, err := kif.Get(ctx, install.ManagerAppName, getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "RoleBinding", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.CoreV1().Secrets(namespace)
			o, err := kif.Get(ctx, install.MutatorWebhookTLSName, getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "Secret", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.CoreV1().Services(namespace)
			o, err := kif.Get(ctx, install.ManagerAppName, getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "Service", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.CoreV1().Services(namespace)
			o, err := kif.Get(ctx, install.AgentInjectorName, getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "Service", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.AdmissionregistrationV1().MutatingWebhookConfigurations()
			o, err := kif.Get(ctx, fmt.Sprintf("%s-webhook-%s", install.AgentInjectorName, namespace), getOpts)
			if err == nil {
				if err = amendObject(&o.ObjectMeta, "MutatingWebhookConfiguration", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
		func() error {
			kif := ki.AppsV1().Deployments(namespace)
			o, err := kif.Get(ctx, install.ManagerAppName, getOpts)
			if err == nil {
				o.ObjectMeta.Labels = selector
				if err = amendObject(&o.ObjectMeta, "Deployment", namespace); err == nil {
					_, err = kif.Update(ctx, o, updOpts)
				}
			}
			return err
		},
	}
}

func amendObject(obj *meta.ObjectMeta, kind, namespace string) error {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app.kubernetes.io/managed-by"] = "Helm"
	obj.SetLabels(labels)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	// Prevent us from taking over an existing release
	// This is really done out of an abundance of caution, as EnsureTrafficManager should validate that there is no existing
	// release before calling importLegacy
	if release, ok := annotations["meta.helm.sh/release-name"]; ok && release != releaseName {
		return fmt.Errorf("refusing to replace existing release annotation %s in %s %s.%s", release, kind, obj.GetName(), obj.GetNamespace())
	}
	if ns, ok := annotations["meta.helm.sh/release-namespace"]; ok && ns != namespace {
		return fmt.Errorf("refusing to replace existing namespace annotation %s in %s %s.%s", ns, kind, obj.GetName(), obj.GetNamespace())
	}
	annotations["meta.helm.sh/release-name"] = releaseName
	annotations["meta.helm.sh/release-namespace"] = namespace
	obj.SetAnnotations(annotations)
	return nil
}

func importLegacy(ctx context.Context, namespace string) error {
	fns := getLegacyFuncs(ctx, namespace)
	wg := sync.WaitGroup{}
	wg.Add(len(fns))
	errs := make(chan error, len(fns))
	for _, fn := range fns {
		fn := fn
		go func() {
			defer wg.Done()
			if err := fn(); !(err == nil || errors.IsNotFound(err)) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	var result error
	for err := range errs {
		dlog.Error(ctx, err)
		result = multierror.Append(result, err)
	}
	return result
}
