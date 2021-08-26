//+build ignore

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/datawire/dlib/dexec"
)

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("Usage: %s $TELEPRESENCE_VERSION", os.Args[0])
	}
	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("unable to get current working directory: %w", err)
	}
	chartSource := filepath.Join(rootDir, "charts", "telepresence")
	if _, err := os.Stat(chartSource); err != nil {
		return fmt.Errorf("failed to stat charts/telepresence (%v) -- this tool must be run from the root of the telepresenceio/telepresence repository", err)
	}
	helm := filepath.Join(rootDir, "tools", "bin", "helm")
	if runtime.GOOS == "windows" {
		helm += ".exe"
	}
	if _, err := os.Stat(helm); err != nil {
		return fmt.Errorf("failed to stat tools/bin/helm (%v); try running \"make tools/bin/helm\"", err)
	}
	version := os.Args[1]
	err = dexec.CommandContext(context.Background(), helm, "package", chartSource, "--version="+version).Run()
	if err != nil {
		return fmt.Errorf("error from helm package: %w", err)
	}
	chartPath := filepath.Join(rootDir, fmt.Sprintf("telepresence-%s.tgz", version))
	dest := filepath.Join(rootDir, "pkg", "install", "helm", "telepresence-chart.tgz")
	err = os.Rename(chartPath, dest)
	if err != nil {
		return fmt.Errorf("unable to move chart: %w", err)
	}
	return nil
}

func main() {
	err := run()
	if err != nil {
		fmt.Printf("failed: %v\n", err)
		os.Exit(1)
	}
}
