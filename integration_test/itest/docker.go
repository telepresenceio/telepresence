package itest

import "runtime"

func MaybeSkipDocker(s *Suite) {
	if !s.IsCI() {
		return
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return
	}
	if runtime.GOOS == "linux" {
		return
	}
	s.T().Skip("CI can't run docker containers inside non-linux runners")
}
