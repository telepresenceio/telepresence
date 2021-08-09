package helm

import (
	"context"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// getHelmRelease gets the traffic-manager helm release; if it is not found, it will return nil
func getHelmRelease(ctx context.Context, helmConfig *action.Configuration) (*release.Release, error) {
	list := action.NewList(helmConfig)
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

func shouldManageRelease(ctx context.Context, rel *release.Release) bool {
	if owner, ok := rel.Config["createdBy"]; ok {
		return owner == releaseOwner
	}
	return false
}

func shouldUpgradeRelease(ctx context.Context, rel *release.Release) bool {
	return rel.Chart.Metadata.Version == client.Version()
}
