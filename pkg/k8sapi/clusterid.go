package k8sapi

import (
	"context"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/derror"
)

// GetClusterID returns the cluster ID for the given cluster.  If there is an error, it still
// returns a usable ID along with the error.
func GetClusterID(ctx context.Context, client *kates.Client) (clusterID string, err error) {
	// Get the ID of the "default" Namespace.
	namespaceID, err := func() (namespaceID string, err error) {
		defer func() {
			// Kates is panicy
			if r := derror.PanicToError(recover()); r != nil {
				err = r
			}
		}()

		ns := &kates.Namespace{
			TypeMeta:   kates.TypeMeta{Kind: "Namespace"},
			ObjectMeta: kates.ObjectMeta{Name: "default"},
		}
		if err := client.Get(ctx, ns, ns); err != nil {
			return "", err
		}

		return string(ns.GetUID()), nil
	}()
	// Just use the namespace ID as the cluster ID.
	if err != nil {
		// But still return a usable ID if there's an error.
		return "00000000-0000-0000-0000-000000000000", err
	}
	return namespaceID, nil
}
