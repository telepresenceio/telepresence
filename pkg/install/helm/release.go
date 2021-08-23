package helm

import (
	"context"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"

	"github.com/blang/semver"

	"github.com/datawire/dlib/dlog"

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
	chartVersion, err := semver.Parse(strings.TrimPrefix(rel.Chart.Metadata.Version, "v"))
	if err != nil {
		dlog.Errorf(ctx, "Could not parse version %s for chart: %v", rel.Chart.Metadata.Version, err)
		return false
	}
	cliVersion := client.Semver()
	if chartVersion.GT(cliVersion) {
		dlog.Warnf(ctx, "You are using Telepresence v%s, but Traffic Manager v%s is installed on the cluster.", cliVersion, chartVersion)
		return false
	}
	// At this point we could also do chartVersion != cliVersion, since chartVersion <= cliVersion
	// But this makes it really clear that we're only doing the upgrade if chartVersion < cliVersion
	return chartVersion.LT(cliVersion)
}
