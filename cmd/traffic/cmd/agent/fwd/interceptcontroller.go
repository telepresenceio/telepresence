package fwd

import (
	"context"
	"errors"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type interceptController struct {
	*manager.InterceptSpec
	id        string
	sessionID tunnel.SessionID
	cancel    context.CancelFunc
	ctx       context.Context
}

func (ic *interceptController) isHTTP() bool {
	return len(ic.HeaderFilters) > 0 || len(ic.PathFilters) > 0
}

type interceptControllerMap map[string]*interceptController

// sorted return the entries in the map, sorted by ID.
func (im interceptControllerMap) sorted() []*interceptController {
	infos := make([]*interceptController, len(im))
	for i, k := range maps.SortedKeys(im) {
		infos[i] = im[k]
	}
	return infos
}

// reconcile updates the interceptControllerMap to match the given interceptInfos and cancels any
// interceptControllers that are no longer in the given list.
func (im interceptControllerMap) reconcile(ctx context.Context, iis []*manager.InterceptInfo) {
	icm := make(map[string]struct{}, len(iis))
	for _, ii := range iis {
		if _, ok := im[ii.Id]; !ok {
			ic := &interceptController{InterceptSpec: ii.Spec, id: ii.Id, sessionID: tunnel.SessionID(ii.ClientSession.SessionId)}
			ic.ctx, ic.cancel = context.WithCancel(ctx)
			what := "intercept"
			if ii.Spec.Wiretap {
				what = "wiretap"
			}
			clog.Debugf(ctx, "Controller for %s %s created", what, ii.Spec.Name)
			im[ii.Id] = ic
		}
		icm[ii.Id] = struct{}{}
	}
	for id, ic := range im {
		if _, ok := icm[id]; !ok {
			what := "intercept"
			if ic.Wiretap {
				what = "wiretap"
			}
			clog.Debugf(ctx, "Controller for %s %s cancelled", what, ic.Name)
			ic.cancel()
			delete(im, id)
		}
	}
}

func (im interceptControllerMap) global() (*interceptController, error) {
	if len(im) > 1 {
		return nil, errors.New("multiple intercepts found when requesting the global intercept")
	}
	for _, ic := range im {
		return ic, nil
	}
	return nil, nil
}

func (im interceptControllerMap) isHTTP() bool {
	for _, ic := range im {
		if ic.isHTTP() {
			return true
		}
	}
	return false
}
