package cloudtoken

import (
	"context"
	"fmt"
	"sync"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"

	apiv1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type Service interface {
	MaybeAddToken(ctx context.Context, apikey string) error
}

type patchConfigmapIfNotPresent struct {
	sync.Mutex
	patchConfigmap func(ctx context.Context, apikey string) error
	watchersDone   <-chan struct{}
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

		// context for subscribe. This must be separated from the watcher ctx
		// because cancelling the subscribe ctx will close the channel, and a case
		// will activate on a closed channel. we dont want search triggered after
		// watchers are shut down
		subCtx, subCancel := context.WithCancel(ctx)

		// search on broadcast
		for {
			select {
			case <-subscribe(subCtx, watchers.mapsCond):
				watchers.searchMaps(cancelCtx, cancel)
			case <-subscribe(subCtx, watchers.secretsCond):
				watchers.searchSecrets(cancelCtx, cancel)
			case <-cancelCtx.Done():
				subCancel()
				return
			}
		}
	}()

	return &patchConfigmapIfNotPresent{
		patchConfigmap: func(ctx context.Context, apikey string) error {
			dlog.Info(ctx, "patching cloud token configmap with apikey")
			_, err := clientset.CoreV1().ConfigMaps(managerns).Patch(
				ctx,
				"traffic-manager-"+CLOUD_TOKEN_NAME_SUFFIX,
				types.StrategicMergePatchType,
				[]byte(fmt.Sprintf(`{"data":{"%s":"%s"}}`, CLOUD_TOKEN_KEY, apikey)),
				apiv1.PatchOptions{},
			)
			if err == nil {
				dlog.Info(ctx, "cloud token configmap successfully patched, stopping cloud token watchers")
				cancel()
			}
			return err
		},
		watchersDone: cancelCtx.Done(),
	}
}

// MaybeAddToken will add a token if one does not already exist.
func (c *patchConfigmapIfNotPresent) MaybeAddToken(ctx context.Context, apikey string) error {
	// prevent multiple patches with lock and cancel
	c.Lock()
	defer c.Unlock()

	select {
	case <-c.watchersDone: // apikey is already present or watcher err, do nothing
		return nil
	default: // patch configmap with token
		return c.patchConfigmap(ctx, apikey)
	}
}
