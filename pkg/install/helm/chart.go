package helm

import (
	"bytes"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"

	telcharts "github.com/telepresenceio/telepresence/v2/charts"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func loadChart() (*chart.Chart, error) {
	var buf bytes.Buffer
	if err := telcharts.WriteChart(&buf, version.Version); err != nil {
		return nil, err
	}
	return loader.LoadArchive(&buf)
}
