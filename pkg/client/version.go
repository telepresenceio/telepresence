package client

import (
	"github.com/blang/semver"

	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Version returns the version of this executable.
func Version() string {
	return version.Version
}

func Semver() semver.Version {
	return version.Structured()
}
