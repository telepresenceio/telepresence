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
	if err := Main(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", os.Args[0], err)
		os.Exit(1)
	}
}

func Main(dstdir string) (err error) {
	maybeSetErr := func(_err error) {
		if err == nil && _err != nil {
			err = _err
		}
	}

	verStr := strings.TrimPrefix(version.Version, "v")

	fh, err := os.OpenFile(filepath.Join(dstdir, "telepresence-"+verStr+".tgz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer func() {
		maybeSetErr(fh.Close())
	}()

	if err := telcharts.WriteChart(fh, version.Version); err != nil {
		return err
	}

	return nil
}
