package resource

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// An Instance exposes CRUD operations for a k8s resource such as
// a Service.
type Instance interface {
	Create(context.Context) error
	Exists(context.Context) (bool, error)
	Update(context.Context) error
	Delete(context.Context) error
}

// scope contains everything that needs to be shared between resources
// during an operation that spans several resources.
type scope struct {
	namespace  string
	clusterID  string
	tmSelector map[string]string
	client     *kates.Client
	env        *client.Env
	caPem      []byte
	crtPem     []byte
	keyPem     []byte
}

// The scope is available through the context
type scopeKey struct{}

func withScope(ctx context.Context, scope *scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

func getScope(ctx context.Context) *scope {
	if sc, ok := ctx.Value(scopeKey{}).(*scope); ok {
		return sc
	}
	return nil
}

type Instances []Instance

// Ensure ensures that all resources are present
func (is Instances) Ensure(ctx context.Context) error {
	for _, in := range is {
		if err := Ensure(ctx, in); err != nil {
			return err
		}
	}
	return nil
}

// Delete deletes the resources in the reverse order of how they
// were added.
func (is Instances) Delete(ctx context.Context) error {
	for i := len(is) - 1; i >= 0; i-- {
		if err := is[i].Delete(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Ensure checks if the given resource exists in which case an attempt
// will be made to update. The resource is created if it doesn't exist.
func Ensure(ctx context.Context, r Instance) error {
	exists, err := r.Exists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return r.Update(ctx)
	}
	return r.Create(ctx)
}

// logName returns the string "<name>.<namespace> <kind>", e.g. "traffic-manager.ambassador Service"
func logName(obj kates.Object) string {
	return fmt.Sprintf("%s %s.%s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), obj.GetNamespace())
}

func create(ctx context.Context, resource kates.Object) error {
	dlog.Infof(ctx, "Creating %s", logName(resource))
	if err := getScope(ctx).client.Create(ctx, resource, nil); err != nil {
		return fmt.Errorf("failed to create %s: %w", logName(resource), err)
	}
	return nil
}

func find(ctx context.Context, resource kates.Object) (kates.Object, error) {
	into := resource.DeepCopyObject().(kates.Object)
	if found, err := findInto(ctx, resource, into); err != nil || !found {
		return nil, err
	}
	return into, nil
}

func exists(ctx context.Context, resource kates.Object) (bool, error) {
	found, err := findInto(ctx, resource, nil)
	if err != nil {
		if errors.IsForbidden(err) {
			// Simply assume that it exists. Not much else we can do unless
			// RBAC is configured to give access.
			return true, nil
		}
		return false, err
	}
	return found, nil
}

func findInto(ctx context.Context, resource, into kates.Object) (bool, error) {
	if err := getScope(ctx).client.Get(ctx, resource, into); err != nil {
		if !kates.IsNotFound(err) {
			return false, fmt.Errorf("failed to get %s: %w", logName(resource), err)
		}
		dlog.Debugf(ctx, "Unable to find %s", logName(resource))
		return false, nil
	}
	dlog.Debugf(ctx, "Found %s", logName(resource))
	return true, nil
}

func remove(ctx context.Context, resource kates.Object) error {
	dlog.Infof(ctx, "Deleting %s", logName(resource))
	if err := getScope(ctx).client.Delete(ctx, resource, nil); err != nil && !kates.IsNotFound(err) {
		return fmt.Errorf("failed to delete %s: %w", logName(resource), err)
	}
	return nil
}
