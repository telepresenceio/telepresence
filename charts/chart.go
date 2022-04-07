package charts

import (
	"archive/tar"
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
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

func addFile(tarWriter *tar.Writer, vfs fs.FS, filename string, content []byte) error {
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

// WriteChart is a minimal `helm package`.
func WriteChart(out io.Writer, version string) error {
	version = strings.TrimPrefix(version, "v")

	var filenames []string
	if err := fs.WalkDir(helmDir, ".", func(filename string, dirent fs.DirEntry, err error) error {
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
			content, err = yaml.Marshal(dat)
			if err != nil {
				return err
			}
			if err := addFile(tarWriter, helmDir, filename, content); err != nil {
				return err
			}
		default:
			content, err := fs.ReadFile(helmDir, filename)
			if err != nil {
				return err
			}
			if err := addFile(tarWriter, helmDir, filename, content); err != nil {
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
