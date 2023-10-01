package helm

import (
	"context"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
)

// getHelmRelease gets the traffic-manager helm release; if it is not found, it will return nil.
func getHelmRelease(ctx context.Context, releaseName string, helmConfig *action.Configuration) (*release.Release, error) {
	list := action.NewList(helmConfig)
	list.Deployed = true
	list.Failed = true
	list.Pending = true
	list.Uninstalled = true
	list.Uninstalling = true
	list.SetStateMask()
	var releases []*release.Release
	err := timedRun(ctx, func(timeout time.Duration) error {
		// The List command never times out, so we need to do it here.
		type rs struct {
			err error
			rs  []*release.Release
		}
		doneCh := make(chan rs)
		go func() {
			rels, err := list.Run()
			doneCh <- rs{err: err, rs: rels}
			close(doneCh)
		}()
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case rr := <-doneCh:
			if rr.err != nil {
				return rr.err
			}
			releases = rr.rs
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	for _, r := range releases {
		if r.Name == releaseName {
			return r, nil
		}
	}
	return nil, nil
}

func releaseVer(rel *release.Release) string {
	return strings.TrimPrefix(rel.Chart.Metadata.Version, "v")
}
