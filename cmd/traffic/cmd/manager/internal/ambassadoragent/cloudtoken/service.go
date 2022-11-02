package cloudtoken

import (
	"context"
	"fmt"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	apiv1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type Service interface {
	MaybeAddToken(ctx context.Context, apikey string) error
}

type patchConfigmapIfNotPresent struct {
	patchConfigmap func(ctx context.Context, apikey string) error
	done           <-chan struct{}
}

const (
	CLOUD_TOKEN_NAME_SUFFIX = "agent-cloud-token"
	CLOUD_TOKEN_KEY         = "CLOUD_CONNECT_TOKEN"
)

// NewPatchConfigmapIfNotPresent will start up watchers to watch for a configmap or secret
// with the apikey. If one is found, the watchers shut down. If MaybeAddToken is called
// and successfully creates a configmap, the watchers are shut down.
func NewPatchConfigmapIfNotPresent(ctx context.Context) *patchConfigmapIfNotPresent {
	managerns := managerutil.GetEnv(ctx).ManagerNamespace
	clientset := k8sapi.GetK8sInterface(ctx)
	watchers := newTokenWatchers(clientset, managerns)

	// ctx for watchers, cancelled if token is found or watcher err
	// cancelling this ctx also stops MaybeAddToken from adding token
	cancelCtx, cancel := context.WithCancel(ctx)

	// watch for secrets and configmaps, cancel if an apikey is found
	go func() {
		// startup and search
		dlog.Info(ctx, "starting cloud token watchers")
		watchers.searchMaps(cancelCtx, cancel)
		watchers.searchSecrets(cancelCtx, cancel)

		// context for subscribe. This has to be different than the watcher context
		// because cancelling the subscribe ctx will close the channel, and a case
		// will activate on a closed channel. we dont want search triggered after
		// watchers are shut down
		subCtx, subCancel := context.WithCancel(ctx)

		// search on broadcast
		for {
			select {
			case <-watchers.mapsWatcher.Subscribe(subCtx):
				watchers.searchMaps(cancelCtx, cancel)
			case <-watchers.secretsWatcher.Subscribe(subCtx):
				watchers.searchSecrets(cancelCtx, cancel)
			case <-cancelCtx.Done():
				subCancel()
				return
			}
		}
	}()

	return &patchConfigmapIfNotPresent{
		patchConfigmap: buildConfigmapPatcher(clientset, managerns),
		done:           cancelCtx.Done(),
	}
}

// MaybeAddToken will add a token if one does not already exist
func (c *patchConfigmapIfNotPresent) MaybeAddToken(ctx context.Context, apikey string) error {
	select {
	case <-c.done: // apikey is already present or watcher err, do nothing
		return nil
	default: // create token
		// we could cancel the watchers here, and have a mutex guard this func,
		// which would prevent multiple simultaneous calls to MaybeAddToken and therefore c.patchToken,
		// but im pretty sure they would just race and the slower one wins, which is fine
		return c.patchConfigmap(ctx, apikey)
	}
}

func buildConfigmapPatcher(clientset kubernetes.Interface, managerns string) func(ctx context.Context, apikey string) error {
	return func(ctx context.Context, apikey string) error {
		dlog.Info(ctx, "patching cloud token configmap with apikey")
		_, err := clientset.CoreV1().ConfigMaps(managerns).Patch(
			ctx,
			"traffic-manager-"+CLOUD_TOKEN_NAME_SUFFIX,
			types.StrategicMergePatchType,
			[]byte(fmt.Sprintf(`{"data":{"%s":"%s"}}`, CLOUD_TOKEN_KEY, apikey)),
			apiv1.PatchOptions{},
		)
		if err == nil {
			dlog.Info(ctx, "cloud token configmap successfully patched")
		}
		return err
	}
}
