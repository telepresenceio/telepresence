package agentmap

import (
	"context"
	"time"

	"github.com/puzpuzpuz/xsync/v3"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
)

type workloadKey struct {
	name      string
	namespace string
}

type workloadValue struct {
	wl    k8sapi.Workload
	err   error
	cTime int64
}

type workloadCache struct {
	*xsync.MapOf[workloadKey, workloadValue]
	maxAge time.Duration
}

type WorkloadCache interface {
	GetWorkload(c context.Context, name, namespace, workloadKind string) (k8sapi.Workload, error)
}

type wlCacheKey struct{}

func WithWorkloadCache(ctx context.Context, maxAge time.Duration) context.Context {
	wc := NewWorkloadCache(ctx, maxAge)
	return context.WithValue(ctx, wlCacheKey{}, wc)
}

func GetWorkloadCache(ctx context.Context) WorkloadCache {
	if wc, ok := ctx.Value(wlCacheKey{}).(WorkloadCache); ok {
		return wc
	}
	return nil
}

func NewWorkloadCache(ctx context.Context, maxAge time.Duration) WorkloadCache {
	w := workloadCache{MapOf: xsync.NewMapOf[workloadKey, workloadValue](), maxAge: maxAge}
	go w.gc(ctx)
	return w
}

func (w workloadCache) gc(ctx context.Context) {
	tc := time.NewTicker(w.maxAge * 5)
	for {
		select {
		case <-ctx.Done():
			tc.Stop()
			return
		case now := <-tc.C:
			minCtime := now.UnixNano() - int64(w.maxAge)
			w.Range(func(key workloadKey, v workloadValue) bool {
				if v.cTime < minCtime {
					w.Delete(key)
				}
				return true
			})
		}
	}
}

func (w workloadCache) GetWorkload(c context.Context, name, namespace, workloadKind string) (k8sapi.Workload, error) {
	dlog.Debugf(c, "GetWorkload(%s,%s,%s)", name, namespace, workloadKind)
	wv, _ := w.Compute(workloadKey{name: name, namespace: namespace}, func(wv workloadValue, loaded bool) (workloadValue, bool) {
		now := time.Now().UnixNano()
		if loaded && wv.cTime >= now-int64(w.maxAge) {
			return wv, false
		}
		wv.wl, wv.err = k8sapi.GetWorkload(c, name, namespace, workloadKind)
		wv.cTime = now
		return wv, false
	})
	return wv.wl, wv.err
}
