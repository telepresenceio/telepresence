package rbacgen

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Render is a client-side render of the chart. Hooks holds the objects of the
// hook manifests. The chart only has pre-upgrade, pre-delete and test hooks,
// so a first install never creates them.
type Render struct {
	Objects []*unstructured.Unstructured
	// Sources holds the template file that rendered each of Objects.
	Sources []string
	Hooks   []*unstructured.Unstructured
}

// All returns the release objects followed by the hook objects.
func (r *Render) All() []*unstructured.Unstructured {
	all := make([]*unstructured.Unstructured, 0, len(r.Objects)+len(r.Hooks))
	all = append(all, r.Objects...)
	return append(all, r.Hooks...)
}

var (
	embeddedChartOnce sync.Once    //nolint:gochecknoglobals // memoization cache
	embeddedChart     *chart.Chart //nolint:gochecknoglobals // memoization cache
	errEmbeddedChart  error        //nolint:gochecknoglobals // memoization cache
)

// loadEmbeddedChart returns the CLI's own embedded telepresence-oss chart at
// version.Structured, loaded once.
func loadEmbeddedChart() (*chart.Chart, error) {
	embeddedChartOnce.Do(func() {
		embeddedChart, errEmbeddedChart = helm.LoadCoreChart(version.Structured)
	})
	return embeddedChart, errEmbeddedChart
}

// ReleaseName is the name of the release that telepresence helm install
// creates.
const ReleaseName = "traffic-manager"

// RenderChart renders the CLI's embedded chart for a release named
// traffic-manager in namespace, without contacting a cluster.
func RenderChart(ctx context.Context, namespace string, values *helm.Values) (*Render, error) {
	chrt, err := loadEmbeddedChart()
	if err != nil {
		return nil, err
	}
	install := action.NewInstall(&action.Configuration{})
	install.DryRun = true
	install.ClientOnly = true
	install.ReleaseName = ReleaseName
	install.Namespace = namespace

	valuesMap, err := values.ToMap()
	if err != nil {
		return nil, err
	}
	rel, err := install.RunWithContext(ctx, chrt, valuesMap)
	if err != nil {
		return nil, err
	}
	r := &Render{}
	if r.Objects, r.Sources, err = decodeSourcedManifests(rel.Manifest); err != nil {
		return nil, err
	}
	for _, h := range rel.Hooks {
		objs, err := decodeManifests(h.Manifest)
		if err != nil {
			return nil, err
		}
		r.Hooks = append(r.Hooks, objs...)
	}
	return r, nil
}

// decodeManifests splits a rendered manifest on YAML document separators and
// decodes each non-empty document into an Unstructured object.
func decodeManifests(manifest string) ([]*unstructured.Unstructured, error) {
	objs, _, err := decodeSourcedManifests(manifest)
	return objs, err
}

const sourcePrefix = "# Source: "

// decodeSourcedManifests is decodeManifests that also returns the template
// file of each object, taken from the "# Source:" comment that Helm puts
// first in every document.
func decodeSourcedManifests(manifest string) ([]*unstructured.Unstructured, []string, error) {
	reader := yamlutil.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	var objs []*unstructured.Unstructured
	var sources []string
	for {
		doc, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, err
		}
		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
			return nil, nil, fmt.Errorf("unable to decode rendered manifest: %w", err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		var source string
		if first, _, _ := bytes.Cut(doc, []byte("\n")); bytes.HasPrefix(first, []byte(sourcePrefix)) {
			source = strings.TrimSpace(string(first[len(sourcePrefix):]))
		}
		objs = append(objs, obj)
		sources = append(sources, source)
	}
	return objs, sources, nil
}
