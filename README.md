This repository contains the <b>Edge ToolKit</b>, or <b>ETK</b> for
short. The <b>ETK</b> is a collection of binaries and golang libraries
for managing requests that that flow into, out of, or within
Kubernetes-based cloud native applications.

## Components of the ETK

Commands:

- [edgectl](docs/edgectl.md) - CLI for controlling the traffic into, out of, or within a Kubernetes cluster
- [teleproxy](docs/teleproxy.md) - connect locally running code to a remote Kubernetes cluster
- [watt](docs/watt.md) - trigger actions when Kubernetes and/or consul resources are updated
- [kubeapply](docs/kubeapply.md) - apply Kubernetes manifests with templating, docker builds, and feedback
- [k3sctl](docs/k3sctl.md) - run/manage a lightweight local Kubernetes cluster and docker registry for testing
- [kubestatus](docs/kubestatus.md) - display and update the status of arbitrary Kubernetes resources

Libraries:

- [supervisor](https://godoc.org/github.com/datawire/teleproxy/pkg/supervisor) - a library (or very lightweight framework) for helping with error handling, startup/shutdown order, and guaranteed resource cleanup amongst groups of related goroutines
- [k8s](https://godoc.org/github.com/datawire/teleproxy/pkg/k8s) - an easy-to-use facade around the client-go library
- [dtest](https://godoc.org/github.com/datawire/teleproxy/pkg/dtest) - testing related utilities

## Changelog

See [CHANGELOG](./CHANGELOG.md) for notes on the current release.

## Linux downloads

```bash
curl https://datawire-static-files.s3.amazonaws.com/edgectl/0.7.0/linux/amd64/edgectl -o edgectl && chmod a+x edgectl
curl https://datawire-static-files.s3.amazonaws.com/teleproxy/0.7.0/linux/amd64/teleproxy -o teleproxy && chmod a+x teleproxy
curl https://datawire-static-files.s3.amazonaws.com/watt/0.7.0/linux/amd64/watt -o watt && chmod a+x watt
curl https://datawire-static-files.s3.amazonaws.com/kubeapply/0.7.0/linux/amd64/kubeapply -o kubeapply && chmod a+x kubeapply
curl https://datawire-static-files.s3.amazonaws.com/k3sctl/0.7.0/linux/amd64/k3sctl -o k3sctl && chmod a+x k3sctl
curl https://datawire-static-files.s3.amazonaws.com/kubestatus/0.7.0/linux/amd64/kubestatus -o kubestatus && chmod a+x kubestatus
```

## Darwin downloads

```bash
curl https://datawire-static-files.s3.amazonaws.com/edgectl/0.7.0/darwin/amd64/edgectl -o edgectl && chmod a+x edgectl
curl https://datawire-static-files.s3.amazonaws.com/teleproxy/0.7.0/darwin/amd64/teleproxy -o teleproxy && chmod a+x teleproxy
curl https://datawire-static-files.s3.amazonaws.com/watt/0.7.0/darwin/amd64/watt -o watt && chmod a+x watt
curl https://datawire-static-files.s3.amazonaws.com/kubeapply/0.7.0/darwin/amd64/kubeapply -o kubeapply && chmod a+x kubeapply
curl https://datawire-static-files.s3.amazonaws.com/k3sctl/0.7.0/darwin/amd64/k3sctl -o k3sctl && chmod a+x k3sctl
curl https://datawire-static-files.s3.amazonaws.com/kubestatus/0.7.0/darwin/amd64/kubestatus -o kubestatus && chmod a+x kubestatus
```

## Repository Organization

This repository follows the standard golang package layout.

#### Commands

You will
find the source code for the binary commands at
`<repo>/cmd/<name>/...`. The main function for each command should be
in a file named `main.go` within the command directory,
e.g. `<repo>/cmd/<name>/main.go`.

#### Packages

Public packages can be found at `<repo>/pkg/<name>/...`, and internal
packages can be found at `<repo>/internal/pkg/<name>/...`.


## Building

The build process uses `jq` to perform some operations. Make sure it is installed:

```console
# On MacOS
$ brew install jq
$ jq --version
jq-1.6
```

Use `make check` and `make build` for running formal test and
builds. You can use `make help` to find other useful targets.

## Developing

The fastest way to install any command from source, e.g. for inner
loop development is to run `go install ./cmd/<name>/...`.

The fastest way to run tests is to run `go test ./<path>/...`

### Testing dependencies

The tests require a Kubernetes cluster and a docker registry to
run. By default these are spun up locally on demand. You can also
supply your own cluster and/or registry.

#### On demand cluster and registry

These are left running in between test runs for performance. If you
want a fully clean test run, you can use `docker ps` and `docker kill`
to clean these up. You can also run `go install ./cmd/k3sctl/...` to
get the `k3sctl` command and use `k3sctl up` and `k3sctl down` to
control these containers. Use the `k3sctl help` command to find other
useful commands, e.g. you can use `k3sctl config -o /tmp/k3s.yaml` to
get a kubeconfig for kusing kubectl against the testing cluster.

#### Supplying your own cluster and registry

Use the `DTEST_REGISTRY` environment variable to make the tests use a
given docker registry for testing. You will need to be authenticated
and have permission to push to whatever registry you supply.

Use the `DTEST_KUBECONFIG` environment variable to make the tests use
a particular Kubernetes cluster for testing. Note that the tests may
be destructive, so make sure you use a suitable cluster. This cluster
will need to be able to pull from whatever registry you supply.

## Releasing

A release consists of binaries for each command uploaded to s3 at the
following URL:

```
s3://datawire-static-files/$*/$(VERSION)/$(GOOS)/$(GOARCH)/$*
```

To do a release, perform the following steps:

1. Make sure to update CHANGELOG.md with any relevant entries since
   the last release.

2. Create a tag with the appropriate version number, e.g. `git tag v1.2.3`.

3. Do a `git push --tags`

4. CI will churn through the build on linux and darwin and build/push
   the appropriate binaries.

To perform a manual release, just type `make release`. The binaries
are platform specific, so you will need to do this on both a mac and a
linux machine in order to have a complete release.

Note that using `make release` you can upload binaries for non-tagged
commits. For a formal release, you should always have a tag of the
form `v<semver>`.

In order to see what `$(VERSION)` will default to, you can type
`make help`, e.g.:

```bash
$ make help
Usage: make [TARGETS...]

  NAME = teleproxy
  VERSION = 0.7.2-16-gd1ca923-dirty
  KUBECONFIG = /tmp/k3s.yaml

TARGETS:
  (Common)     build     Build the software
  (Common)     check     Check whether the software works; run the tests
  (Common)     clean     Delete all files that are normally created by building the software
  (Common)     clobber   Delete all files that this Makefile can re-generate
  (Common)     format    Apply automatic formatting+cleanup to source code
  (Common)     help      Show this message
  (Common)     lint      Perform static analysis of the software
  (Go)         go-build  Build the code with `go build`
  (Go)         go-doc    Run a `godoc -http` server
  (Go)         go-fmt    Fixup the code with `go fmt`
  (Go)         go-get    Download Go dependencies
  (Go)         go-lint   Check the code with `golangci-lint`
  (Go)         go-test   Check the code with `go test`
  (teleproxy)  release   Upload binaries to S3
$ 
```
