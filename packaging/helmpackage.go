package main

import (
	"flag"
	"log"
	"os"

	"github.com/blang/semver/v4"

	telcharts "github.com/telepresenceio/telepresence/v2/charts"
)

type sv semver.Version

func (v *sv) String() string {
	return (*semver.Version)(v).String()
}

func (v *sv) Set(s string) error {
	ver, err := semver.Parse(s)
	if err == nil {
		*v = sv(ver)
	}
	return err
}

func main() {
	var output string
	var version sv
	flag.StringVar(&output, "o", "", "output file")
	flag.Var(&version, "v", "Helm chart version")
	flag.Parse()
	err := packageHelmChart(output, semver.Version(version))
	if err != nil {
		log.Fatal(err)
	}
}

func packageHelmChart(filename string, version semver.Version) error {
	fh, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer fh.Close()
	return telcharts.WriteChart(telcharts.DirTypeTelepresence, fh, "telepresence", version.String())
}
