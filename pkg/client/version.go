package client

import (
	"fmt"

	"github.com/blang/semver"

	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Version returns the version of this executable.
func Version() string {
	return version.Version
}

func Semver() semver.Version {
	v := Version()
	sv, err := semver.ParseTolerant(v)
	if err != nil {
		panic(fmt.Sprintf("this binary's version is unparsable: %v", err))
	}
	return sv
}
