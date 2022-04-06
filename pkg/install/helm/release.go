package helm

import (
	"context"
	"strings"

	"github.com/blang/semver"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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

func shouldManageRelease(ctx context.Context, rel *release.Release) bool {
	if owner, ok := rel.Config["createdBy"]; ok {
		return owner == releaseOwner
	}
	return false
}

func releaseNeedsCleanup(ctx context.Context, rel *release.Release) bool {
	dlog.Debugf(ctx, "Traffic Manager release %s was found to be in status %s", releaseVer(rel), rel.Info.Status)
	return rel.Info.Status != release.StatusDeployed
}

func shouldUpgradeRelease(ctx context.Context, rel *release.Release) bool {
	ver := releaseVer(rel)
	chartVersion, err := semver.Parse(ver)
	if err != nil {
		dlog.Errorf(ctx, "Could not parse version %s for chart: %v", ver, err)
		return false
	}
	cliVersion := client.Semver()
	if chartVersion.GT(cliVersion) {
		dlog.Warnf(ctx, "You are using Telepresence %s, but Traffic Manager %s is installed on the cluster.", cliVersion, ver)
		return true
	}
	// At this point we could also do chartVersion != cliVersion, since chartVersion <= cliVersion
	// But this makes it really clear that we're only doing the upgrade if chartVersion < cliVersion
	return chartVersion.LT(cliVersion)
}

func releaseVer(rel *release.Release) string {
	return strings.TrimPrefix(rel.Chart.Metadata.Version, "v")
}
