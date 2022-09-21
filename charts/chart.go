package charts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
	"sigs.k8s.io/yaml"
)

//go:embed all:telepresence
var helmDir embed.FS

// filePriority returns the sort-priority of a filename; higher priority files sorts earlier.
func filePriority(filename string) int {
	prio := map[string]int{
		"telepresence/Chart.yaml":         4,
		"telepresence/values.yaml":        3,
		"telepresence/values.schema.json": 2,
		// "telepresence/templates/**":    1,
		// "otherwise":                    0,
	}[filename]
	if prio == 0 && strings.HasPrefix(filename, "telepresence/templates/") {
		prio = 1
	}
	return prio
}

func addFile(tarWriter *tar.Writer, vfs fs.FS, filename string, content io.Reader) error {
	// Build the tar.Header.
	fi, err := fs.Stat(vfs, filename)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	header.Name = filename
	header.Mode = 0644
	header.Size = fi.Size()

	if br, ok := content.(*bytes.Reader); ok {
		header.Size = br.Size()
	}

	// Write the tar.Header.
	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}

	// Write the content.
	if _, err := io.Copy(tarWriter, content); err != nil {
		return err
	}

	return nil
}

// WriteChart is a minimal `helm package`.
func WriteChart(out io.Writer, version string) error {
	version = strings.TrimPrefix(version, "v")

	var filenames []string
	if err := fs.WalkDir(helmDir, ".", func(filename string, dirent fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirent.Type().IsDir() && dirent.Name() == "charts" {
			return filepath.SkipDir
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
		iPrio := filePriority(iName)
		jPrio := filePriority(jName)
		if d := iPrio - jPrio; d != 0 {
			return d > 0
		}

		// priority is the same
		return iName < jName
	})

	zipper := gzip.NewWriter(out)
	zipper.Header.Extra = []byte("+aHR0cHM6Ly95b3V0dS5iZS96OVV6MWljandyTQo=") // magic number for Helm
	zipper.Header.Comment = "Helm"

	tarWriter := tar.NewWriter(zipper)

	agentVersion := ""
	for _, filename := range filenames {
		switch filename {
		case "telepresence/Chart.yaml":
			content, err := fs.ReadFile(helmDir, filename)
			if err != nil {
				return err
			}
			var dat chart.Metadata
			if err := yaml.Unmarshal(content, &dat); err != nil {
				return err
			}
			dat.Version = version
			dat.AppVersion = version

			for _, dep := range dat.Dependencies {
				if dep.Name == "ambassador-agent" {
					agentVersion = dep.Version
					break
				}
			}

			content, err = yaml.Marshal(dat)
			if err != nil {
				return err
			}
			if err := addFile(tarWriter, helmDir, filename, bytes.NewReader(content)); err != nil {
				return err
			}
		default:
			content, err := fs.ReadFile(helmDir, filename)
			if err != nil {
				return err
			}
			if err := addFile(tarWriter, helmDir, filename, bytes.NewReader(content)); err != nil {
				return err
			}
		}
	}

	a8rAgentCacheDir, err := os.MkdirTemp("", "")
	if err != nil {
		return err
	}
	aacu := ambassadadorAgentChartUpdate{
		chartName: "ambassador-agent",
		repoName:  "datawire",
		repoURL:   "https://getambassador.io",
		cacheDir:  a8rAgentCacheDir,
		chartDir:  filepath.Join(a8rAgentCacheDir, "telepresence/charts"),
		version:   agentVersion,
	}
	a8rAgentChartName, err := aacu.execute()
	if err != nil {
		return err
	}

	agentChartFile, err := os.Open(filepath.Join(aacu.chartDir, a8rAgentChartName))
	if err != nil {
		return err
	}
	defer agentChartFile.Close()
	a8rAgentChartPath := filepath.Join("telepresence/charts", a8rAgentChartName)
	err = addFile(tarWriter, os.DirFS(aacu.cacheDir), a8rAgentChartPath, agentChartFile)
	if err != nil {
		return err
	}

	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := zipper.Close(); err != nil {
		return err
	}

	return nil
}

type ambassadadorAgentChartUpdate struct {
	cacheDir  string
	repoName  string
	chartName string
	chartDir  string
	repoURL   string
	version   string
}

func (aacu ambassadadorAgentChartUpdate) execute() (string, error) {
	settings := cli.New()
	entry := repo.Entry{
		Name: aacu.repoName,
		URL:  aacu.repoURL,
	}

	chartRepo, err := repo.NewChartRepository(&entry, getter.All(settings))
	if err != nil {
		return "", err
	}
	chartRepo.CachePath = aacu.cacheDir

	indexFilePath, err := chartRepo.DownloadIndexFile()
	if err != nil {
		return "", err
	}

	indexFile, err := repo.LoadIndexFile(indexFilePath)
	if err != nil {
		return "", err
	}

	var a8rAgentCharts repo.ChartVersions
	for name, charts := range indexFile.Entries {
		if name == aacu.chartName {
			a8rAgentCharts = charts
			break
		}
	}

	if len(a8rAgentCharts) == 0 {
		return "", fmt.Errorf("no charts versions found for %s", aacu.chartName)
	}

	var chart *repo.ChartVersion
	switch aacu.version {
	case "": // get latest version
		sort.Sort(a8rAgentCharts)
		chart = a8rAgentCharts[len(a8rAgentCharts)-1]
	default:
		chartVersion := strings.TrimPrefix(aacu.version, "v")
		for _, c := range a8rAgentCharts {
			if c.Version == chartVersion {
				chart = c
			}
		}
	}

	if chart == nil {
		return "", fmt.Errorf("unable to obtain chart")
	}

	chartURL := chart.URLs[0]
	response, err := http.Get(chartURL)
	if err != nil {
		return "", err
	}

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("received non 200 status code: %d", response.StatusCode)
	}

	defer response.Body.Close()

	if err := os.MkdirAll(aacu.chartDir, 0700); err != nil {
		return "", err
	}

	chartFileName := path.Base(chartURL)
	chartFilePath := filepath.Join(aacu.chartDir, chartFileName)
	file, err := os.Create(chartFilePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.Copy(file, response.Body)
	if err != nil {
		return "", err
	}

	return chartFileName, nil
}
