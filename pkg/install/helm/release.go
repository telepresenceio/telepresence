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

func shouldUpgradeRelease(ctx context.Context, rel *release.Release) bool {
	ver := releaseVer(rel)
	chartVersion, err := semver.Parse(ver)
	if err != nil {
		dlog.Errorf(ctx, "Could not parse version %s for chart: %v", ver, err)
		return false
	}
	cliVersion := client.Semver()

	switch chartVersion.Compare(cliVersion) {
	case -1: // chartVersion < cliVersion (so chart needs an upgrade)
		return true
	case 0: // chartVersion == cliVersion
		return false
	case 1: // chartVersion > cliVersion
		dlog.Warnf(ctx, "You are using Telepresence %s, but Traffic Manager %s is installed on the cluster.", cliVersion, ver)
		return false
	}
	panic("not reached")
}

func releaseVer(rel *release.Release) string {
	return strings.TrimPrefix(rel.Chart.Metadata.Version, "v")
}
