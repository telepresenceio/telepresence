package helm

import (
	"bytes"
	_ "embed" // embed needs to be imported for the go:embed directive to work

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

//go:embed telepresence-chart.tgz
var telepresenceChart []byte

func loadChart() (*chart.Chart, error) {
	return loader.LoadArchive(bytes.NewReader(telepresenceChart))
}
