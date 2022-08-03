package helm

import (
	"context"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
)

// getHelmRelease gets the traffic-manager helm release; if it is not found, it will return nil
func getHelmRelease(ctx context.Context, helmConfig *action.Configuration) (*release.Release, error) {
	list := action.NewList(helmConfig)
	list.Deployed = true
	list.Failed = true
	list.Pending = true
	list.Uninstalled = true
	list.Uninstalling = true
	list.SetStateMask()
	releases, err := list.Run()
	if err != nil {
		return nil, err
	}
	var release *release.Release
	for _, r := range releases {
		if r.Name == releaseName {
			release = r
			break
		}
	}
	return release, nil
}

func releaseVer(rel *release.Release) string {
	return strings.TrimPrefix(rel.Chart.Metadata.Version, "v")
}
