package charts

import (
	"archive/tar"
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/spf13/afero"
	"helm.sh/helm/v3/pkg/chart"
	"sigs.k8s.io/yaml"
)

type DirType int8

const (
	DirTypeTelepresence   DirType = iota
	TelepresenceChartName         = "telepresence-oss"
)

//go:embed all:telepresence-oss
var TelepresenceFS embed.FS

// filePriority returns the sort-priority of a filename; higher priority files sorts earlier.
func filePriority(chartName, filename string) int {
	prio := map[string]int{
		fmt.Sprintf("%s/Chart.yaml)", chartName):        4,
		fmt.Sprintf("%s/values.yaml)", chartName):       3,
		fmt.Sprintf("%s/values.schema.json", chartName): 2,
		// "telepresence/templates-oss/**":    1,
		// "otherwise":                        0,
	}[filename]
	if prio == 0 && strings.HasPrefix(filename, fmt.Sprintf("%s/templates/", chartName)) {
		prio = 1
	}
	return prio
}

func addFile(tarWriter *tar.Writer, vfs fs.FS, filename string, content []byte) error {
	var header *tar.Header
	// Build the tar.Header.
	fi, err := fs.Stat(vfs, filename)
	if err == nil {
		header, err = tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
	} else {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		header = &tar.Header{}
	}
	header.Name = filename
	header.Mode = 0o644
	header.Size = int64(len(content))

	// Write the tar.Header.
	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}

	// Write the content.
	if _, err := tarWriter.Write(content); err != nil {
		return err
	}

	return nil
}

type ChartOverlayFuncDef func(base afero.Fs) (afero.Fs, error)

// ChartOverlayFunc can be used by module extensions to add or overwrite the charts directory.
// type ChartOverlayFunc func(base afero.Fs) (afero.Fs, error).
var ChartOverlayFunc map[DirType]ChartOverlayFuncDef //nolint:gochecknoglobals // extension point

// WriteChart is a minimal `helm package`.
func WriteChart(helmChartDir DirType, out io.Writer, chartName, version string, overlays ...fs.FS) error {
	embedChart := map[DirType]embed.FS{
		DirTypeTelepresence: TelepresenceFS,
	}[helmChartDir]

	var baseDir fs.FS = embedChart
	if chartOverlayFunc, ok := ChartOverlayFunc[helmChartDir]; ok {
		base := afero.FromIOFS{FS: embedChart}
		ovl, err := chartOverlayFunc(base)
		if err != nil {
			return err
		}
		baseDir = afero.NewIOFS(afero.NewCopyOnWriteFs(base, ovl))
	}

	version = strings.TrimPrefix(version, "v")

	var filenames []string
	if err := fs.WalkDir(baseDir, ".", func(filename string, dirent fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirent.Type().IsRegular() {
			filenames = append(filenames, filename)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(filenames, func(i, j int) bool {
		iName := filenames[i]
		jName := filenames[j]

		// higher priority files sorts earlier.
		iPrio := filePriority(chartName, iName)
		jPrio := filePriority(chartName, jName)
		if d := iPrio - jPrio; d != 0 {
			return d > 0
		}

		// priority is the same
		return iName < jName
	})

	zipper := gzip.NewWriter(out)
	zipper.Extra = []byte("+aHR0cHM6Ly95b3V0dS5iZS96OVV6MWljandyTQo=") // magic number for Helm
	zipper.Comment = "Helm"

	tarWriter := tar.NewWriter(zipper)

	for _, filename := range filenames {
		switch filename {
		case fmt.Sprintf("%s/values.schema.yaml", chartName):
			content, err := fs.ReadFile(baseDir, filename)
			if err != nil {
				return err
			}
			defs, err := fs.ReadFile(baseDir, fmt.Sprintf("%s/k8s-defs.json", chartName))
			if err != nil {
				return err
			}
			contentMap := make(map[string]any)
			err = yaml.Unmarshal(content, &contentMap)
			if err != nil {
				return err
			}
			defsMap := make(map[string]any)
			err = json.Unmarshal(defs, &defsMap)
			if err != nil {
				return err
			}
			contentMap["definitions"] = defsMap["definitions"]
			content, err = json.Marshal(contentMap)
			if err != nil {
				return err
			}
			if err = addFile(tarWriter, baseDir, fmt.Sprintf("%s/values.schema.json", chartName), content); err != nil {
				return err
			}
		case fmt.Sprintf("%s/Chart.yaml", chartName):
			content, err := fs.ReadFile(baseDir, filename)
			if err != nil {
				return err
			}
			var dat chart.Metadata
			if err := yaml.Unmarshal(content, &dat); err != nil {
				return err
			}
			dat.Version = version
			dat.AppVersion = version
			content, err = yaml.Marshal(dat)
			if err != nil {
				return err
			}
			if err = addFile(tarWriter, baseDir, filename, content); err != nil {
				return err
			}
		case fmt.Sprintf("%s/k8s-defs.json", chartName):
			// Don't include k8s-defs.json to the chart package
		default:
			content, err := fs.ReadFile(baseDir, filename)
			if err != nil {
				return err
			}
			if err = addFile(tarWriter, baseDir, filename, content); err != nil {
				return err
			}
		}
	}

	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := zipper.Close(); err != nil {
		return err
	}

	return nil
}
