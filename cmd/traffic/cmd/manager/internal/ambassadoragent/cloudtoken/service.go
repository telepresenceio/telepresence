package cloudtoken

import (
	"context"
	"fmt"
	"strings"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	apiv1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type Service interface {
	MaybeAddToken(ctx context.Context, apikey string) error
}

type createConfigmapIfNotPresent struct {
	createToken func(ctx context.Context, apikey string) error
	done        <-chan struct{}
}

var (
	suffix               = "agent-cloud-token"
	cloudConnectTokenKey = "CLOUD_CONNECT_TOKEN"
)

// NewCreateConfigmapIfNotPresent will start up watchers to watch for a configmap or secret
// with the apikey. If one is found, the watchers shut down. If MaybeAddToken is called
// and successfully creates a configmap, the watchers are shut down.
func NewCreateConfigmapIfNotPresent(ctx context.Context) *createConfigmapIfNotPresent {
	managerns := managerutil.GetEnv(ctx).ManagerNamespace
	clientset := k8sapi.GetK8sInterface(ctx)
	watchers := newTokenWatchers(clientset, managerns)

	// ctx for watchers, cancelled if token is found
	cancelCtx, cancel := context.WithCancel(ctx)

	go func() {
		// search checks if apikey token exists, if it does, shut down the watchers
		search := func() {
			configmaps, err := watchers.mapsWatcher.List(cancelCtx)
			if err != nil {
				dlog.Errorf(ctx, "error watching cloud token configmaps: %s", err.Error())
				cancel()
			}
			for _, cm := range configmaps {
				_, ok := cm.Data[cloudConnectTokenKey]
				if strings.HasSuffix(cm.Name, suffix) && ok {
					dlog.Infof(ctx, "configmap %s found, stopping cloud token watchers", cm.Name)
					cancel()
				}
			}
			secrets, err := watchers.secretsWatcher.List(cancelCtx)
			if err != nil {
				dlog.Errorf(ctx, "error watching cloud token secrets: %s", err.Error())
				cancel()
			}
			for _, s := range secrets {
				_, ok := s.Data[cloudConnectTokenKey]
				if strings.HasSuffix(s.Name, suffix) && ok {
					dlog.Infof(ctx, "secret %s found, stopping cloud token watchers", s.Name)
					cancel()
				}
			}
		}

		// startup and search
		dlog.Info(ctx, "starting cloud token watchers")
		search()

		// context for subscribe. This has to be different than the watcher context
		// because cancelling the subscribe ctx will close the channel, and a case
		// will activate on a closed channel. we dont want search triggered after
		// watchers are shut down
		subCtx, subCancel := context.WithCancel(ctx)

		// search on broadcast
		for {
			select {
			case <-watchers.subscribe(subCtx):
				search()
			case <-cancelCtx.Done():
				subCancel()
				return
			}
		}
	}()

	return &createConfigmapIfNotPresent{
		createToken: func(ctx context.Context, apikey string) error {
			dlog.Info(ctx, "patching cloud token configmap with apikey")
			_, err := clientset.CoreV1().ConfigMaps(managerns).Patch(
				ctx,
				"traffic-manager-"+suffix,
				types.StrategicMergePatchType,
				[]byte(fmt.Sprintf(`{"data":{"%s":"%s"}}`, cloudConnectTokenKey, apikey)),
				apiv1.PatchOptions{},
			)
			if err == nil {
				dlog.Info(ctx, "cloud token configmap successfully patched")
			}
			return err
		},
		done: cancelCtx.Done(),
	}
}

// MaybeAddToken will add a token if one does not already exist
func (c *createConfigmapIfNotPresent) MaybeAddToken(ctx context.Context, apikey string) error {
	select {
	case <-c.done: // apikey is already present or watcher err, do nothing
		return nil
	default: // create token
		// we could cancel the watchers here, and have a mutex guard this func,
		// which would prevent multiple calls to MaybeAddToken and therefore c.createToken,
		// but im pretty sure it would just race and the loser gets logged as err :shrug:
		return c.createToken(ctx, apikey)
	}
}
