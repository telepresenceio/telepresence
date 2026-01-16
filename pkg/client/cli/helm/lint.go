package helm

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/blang/semver/v4"
	"helm.sh/helm/v3/pkg/action"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/telepresence/v2/charts"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// lint checks that the chart is valid.
func lint(ctx context.Context, clientGetter genericclioptions.RESTClientGetter, namespace string, req *Request) error {
	vals, err := coalesceValues(ctx, req)
	if err != nil {
		return err
	}

	tmVer, err := getTrafficManagerVersion(vals)
	if err != nil {
		return err
	}
	if req.Version != "" {
		ver, err := semver.ParseTolerant(req.Version)
		if err != nil {
			return fmt.Errorf("unable to parse chart version %q: %v", req.Version, err)
		}
		if !ver.EQ(tmVer) {
			helmConfig, err := getHelmConfig(ctx, clientGetter, namespace)
			if err != nil {
				return fmt.Errorf("failed to initialize helm config: %w", err)
			}
			return withDownloadedChart(ctx, helmConfig, client.GetConfig(ctx).Helm().ChartURL, ver, func(s string) error {
				return runLint(s, namespace, vals, req)
			})
		}
	}
	fh, err := os.CreateTemp("", fmt.Sprintf("%s-*.tgz", charts.TelepresenceChartName))
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(fh.Name())
	}()
	err = charts.WriteChart(charts.DirTypeTelepresence, fh, charts.TelepresenceChartName, tmVer)
	fh.Close()
	if err != nil {
		return err
	}
	return runLint(fh.Name(), namespace, vals, req)
}

func runLint(path, namespace string, vals map[string]any, req *Request) error {
	lint := action.NewLint()
	lint.Namespace = namespace
	lint.Strict = true
	lint.KubeVersion = req.KubeVersion
	lr := lint.Run([]string{path}, vals)
	if err := errors.Join(lr.Errors...); err != nil {
		return err
	}
	for _, msg := range lr.Messages {
		ioutil.Println(os.Stdout, msg)
	}
	return nil
}
