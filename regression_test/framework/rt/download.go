package rt

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/blang/semver/v4"
)

// downloadBinary fetches the released telepresence CLI binary for v from
// its GitHub release and returns the path to the executable, caching it
// under build-output/rtest/downloads so repeated runs against the same
// RTEST_CLIENT_VERSION skip the network round trip. Mirrors
// integration_test/itest/cluster.go:327's downloadBinary: same URL shape,
// same zip handling on Windows.
func downloadBinary(ctx context.Context, buildOutput string, v semver.Version) (string, error) {
	dir := filepath.Join(buildOutput, "rtest", "downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("rtest: creating %s: %w", dir, err)
	}
	cdPath := filepath.Join(dir, "telepresence-"+v.String())
	if runtime.GOOS == "windows" {
		exe := filepath.Join(cdPath, "telepresence.exe")
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	} else if _, err := os.Stat(cdPath); err == nil {
		return cdPath, nil
	}

	url := fmt.Sprintf("https://github.com/telepresenceio/telepresence/releases/download/v%s/telepresence-%s-%s",
		v, runtime.GOOS, runtime.GOARCH)
	dlPath := cdPath
	if runtime.GOOS == "windows" {
		url += ".zip"
		dlPath = cdPath + ".zip"
	}

	if err := fetchToFile(ctx, url, dlPath); err != nil {
		return "", err
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(dlPath, 0o755); err != nil {
			return "", fmt.Errorf("rtest: chmod %s: %w", dlPath, err)
		}
		return dlPath, nil
	}

	if err := unzip(dlPath, cdPath); err != nil {
		return "", err
	}
	return filepath.Join(cdPath, "telepresence.exe"), nil
}

// fetchToFile downloads url and writes its body to path.
func fetchToFile(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("rtest: building request for %s: %w", url, err)
	}
	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("rtest: downloading %s: %w", url, err)
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusOK {
		return fmt.Errorf("rtest: downloading %s: %s", url, rsp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("rtest: creating %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, rsp.Body); err != nil {
		return fmt.Errorf("rtest: writing %s: %w", path, err)
	}
	return nil
}

// unzip extracts the zip archive at zipFile into dir, which it creates if
// necessary. Used for the Windows release asset, which ships as a zip
// rather than a bare executable.
func unzip(zipFile, dir string) error {
	uz, err := zip.OpenReader(zipFile)
	if err != nil {
		return fmt.Errorf("rtest: opening %s: %w", zipFile, err)
	}
	defer uz.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("rtest: creating %s: %w", dir, err)
	}
	for _, f := range uz.File {
		if err := unzipEntry(dir, f); err != nil {
			return err
		}
	}
	return nil
}

func unzipEntry(dir string, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("rtest: opening zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(filepath.Join(dir, f.Name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.FileInfo().Mode())
	if err != nil {
		return fmt.Errorf("rtest: creating %s: %w", f.Name, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("rtest: extracting %s: %w", f.Name, err)
	}
	return nil
}
