module github.com/datawire/build-aux/bin-go/flock

go 1.12

// Because we use this to safely compile other Go programs with Go
// 1.11 on systems that don't have a native flock(1) program
// (i.e. macOS systems with Go 1.11), it is important that this
// implementation of flock has no module dependencies, because it
// isn't yet safe to access the module cache.
