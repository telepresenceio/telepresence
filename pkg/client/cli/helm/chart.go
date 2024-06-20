package helm

import (
	"bytes"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"

	telcharts "github.com/telepresenceio/telepresence/v2/charts"
)

func loadCoreChart(version string) (*chart.Chart, error) {
	var buf bytes.Buffer
	if err := telcharts.WriteChart(telcharts.DirTypeTelepresence, &buf, "telepresence", version); err != nil {
		return nil, err
	}
	return loader.LoadArchive(&buf)
}

func loadCRDChart(version string) (*chart.Chart, error) {
	var buf bytes.Buffer
	if err := telcharts.WriteChart(telcharts.DirTypeTelepresenceCRDs, &buf, "telepresence-crds", version); err != nil {
		return nil, err
	}
	return loader.LoadArchive(&buf)
}
