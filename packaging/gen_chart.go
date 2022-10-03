// gen_chart.go is essentially poor-man's `helm package`.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	telcharts "github.com/telepresenceio/telepresence/v2/charts"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func main() {
	var version string
	if len(os.Args) > 2 {
		version = os.Args[2]
	}
	if err := Main(os.Args[1], version); err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", os.Args[0], err)
		os.Exit(1)
	}
}

func Main(dstdir, verStr string) (err error) {
	maybeSetErr := func(_err error) {
		if err == nil && _err != nil {
			err = _err
		}
	}

	if verStr == "" {
		verStr = version.Version
	}
	verStr = strings.TrimPrefix(verStr, "v")

	fh, err := os.OpenFile(filepath.Join(dstdir, "telepresence-"+verStr+".tgz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	defer func() {
		maybeSetErr(fh.Close())
	}()

	if err := telcharts.WriteChart(fh, "v"+verStr); err != nil {
		return err
	}

	return nil
}
