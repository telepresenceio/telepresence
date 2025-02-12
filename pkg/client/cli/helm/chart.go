package helm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"

	"github.com/datawire/dlib/dlog"
	telcharts "github.com/telepresenceio/telepresence/v2/charts"
)

func loadCoreChart(version string) (*chart.Chart, error) {
	var buf bytes.Buffer
	if err := telcharts.WriteChart(telcharts.DirTypeTelepresence, &buf, "telepresence", version); err != nil {
		return nil, err
	}
	return loader.LoadArchive(&buf)
}

func newDefaultRegistryClient(ctx context.Context) (*registry.Client, error) {
	return registry.NewClient(
		registry.ClientOptDebug(dlog.MaxLogLevel(ctx) >= dlog.LogLevelDebug),
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(dlog.StdLogger(ctx, dlog.LogLevelDebug).Writer()),
	)
}

func pullCoreChart(ctx context.Context, helmConfig *action.Configuration, ref, version string) (*chart.Chart, error) {
	client, err := newDefaultRegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "helm-")
	if err != nil {
		return nil, err
	}
	defer func() {
		err = os.RemoveAll(dir)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()
	pull := action.NewPullWithOpts(action.WithConfig(helmConfig))
	pull.Version = version
	pull.DestDir = dir
	pull.Settings = cli.New()
	pull.SetRegistryClient(client)
	out, err := pull.Run(ref)
	dlog.Info(ctx, out)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, fmt.Sprintf("telepresence-oss-%s.tgz", version)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return loader.LoadArchive(f)
}

func loadCRDChart(version string) (*chart.Chart, error) {
	var buf bytes.Buffer
	if err := telcharts.WriteChart(telcharts.DirTypeTelepresenceCRDs, &buf, "telepresence-crds", version); err != nil {
		return nil, err
	}
	return loader.LoadArchive(&buf)
}
