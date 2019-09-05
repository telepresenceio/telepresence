<!-- -*- fill-column: 100 -*- -->
# Datawire build-aux CHANGELOG

 - 2019-08-14: `go-mod.mk`: `go.bins`, `go.list`: Correctly prune nested Go modules in git
               submodules.

 - 2019-08-14: `docker.mk`: Try to run `docker build` with `--pull` by default.
 - 2019-08-14: `docker.mk`: Allow overriding the tag expression on a per-image basis.
 - 2019-08-14: BREAKING CHANGE: `docker.mk`, `docker-cluster.mk`: Overhaul how tagging an pushing
               works, to be more flexible:
                * Have to call the `docker.tag.rule` macro, instead of setting the `DOCKER_IMAGE`
                  variable.
                * Images are not tagged by default; you must now depend on `NAME.docker.tag.GROUP`
                  (for a GROUP set up with `docker.tag.rule`).
                * To push to an in-cluster repo is now `NAME.docker.push.cluster`, instead of
                  `NAME.docker.knaut-push`.
                * To push to a public repo is now `NAME.docker.push.GROUP` (for a GROUP set up with
                  `docker.tag.rule`), instead of `NAME.docker.push`.
 - 2019-08-13: BREAKING CHANGE: `docker.mk`: The in-cluster private registry stuff has moved to
               `docker-cluster.mk`.
 - 2019-08-13: BREAKING CHANGE: `prelude.mk`: The `build-aux.go-build` macro has been removed in
               favor of the `build-aux.bin-go.rule` macro.

 - 2019-08-13: Fix race condition in `make clobber` where it attempted to use compiled programs used
               for cleanup, after the programs themselves had already been deleted.

 - 2019-07-10: `var.mk`: Introduce

 - 2019-07-05: `build-aux-push`: Work around problem with `git subtree`; avoid accidentally pushing
               proprietary code to build-aux.git.

 - 2019-07-03: `go-mod.mk`: `.opensource.tar.gz` files are still part of `make build`, but no longer
               part of `make go-build`.

 - 2019-07-03: Rewrite the `go-opensource` Bash script as `go-mkopensource` in Go.

 - 2019-07-03: Migrate from `curl` to `go.mod`.
 - 2019-07-03: BREAKING CHANGE: Move executables to be in `./build-aux/bin/` instead of directly in
               `./build-aux/`.  Each of these programs now has a variable to refer to it by, instead
               of having to hard-code the path.  It is also no longer valid to use one of those
               programs without depending on it first.

 - 2019-06-20: `go-mod.mk`: Bump golangci-lint version 1.15.0→1.17.1.
 - 2019-06-20: `go-mod.mk`: For each binary, generate a `BIN.opensource.tar.gz` file.
 - 2019-06-20: `go-workspace.mk`: Remove.

 - 2019-05-31: `go-mod.mk`: Add `go doc` target to run `godoc -http`.

 - 2019-05-01: BREAKING CHANGE: `docker.mk`: Don't include `kuberanut-ui.mk`.
 - 2019-05-01: BREAKING CHANGE: `teleproxy.mk`: Don't include `kuberanut-ui.mk`.
 - 2019-05-01: Go over documented inputs/outputs; differentiate between "eager" and "lazy inputs".
 - 2019-05-01: BREAKING CHANGE: `help.mk`: Don't include `common.mk`.
 - 2019-05-01: BREAKING CHANGE: `kubeapply.mk`: Don't include `common.mk`.
 - 2019-05-01: BREAKING CHANGE: `kubernaut.mk`: Don't include `common.mk`.
 - 2019-05-01: BREAKING CHANGE: `flock.mk`: Delete; merge in to `prelude.mk`.
 - 2019-05-01: Drop dependency on Go for setting `GO{HOST,}{OS,ARCH}`.
 - 2019-05-01: BREAKING CHANGE: Use GOHOSTOS/GOHOSTARCH instead of GOOS/GOARCH as appropriate.
 - 2019-05-01: `prelude.mk`: Add `$(call lazyonce,…)`.
 - 2019-05-01: `prelude.mk`: Introduce, steal code from `common.mk`.

 - 2019-02-15: `kubernaut-ui.mk`: Avoid warnings from `make` inside of `make shell`.

 - 2019-02-13: `_go-common.mk`: Bump golangci-lint version 1.13.1→1.15.0.

 - 2019-02-08: `common.mk`: Slight rework of how test dependencies work.

 - 2019-01-30: `common.mk`: Add TAP-based `check` infrastructure.

 - 2019-01-23: `docker.mk`: Fix `.knaut.push` on macOS.
 - 2019-01-18: `docker.mk`: Robustness improvements.

 - 2019-01-15: `go.mk`: Remove this symlink to `go-workspace.mk`.

 - 2019-01-10: `teleproxy.mk`: Go to simple 1s polling, instead of exponential backoff.

 - 2019-01-05: `build-aux-push`: Rejoin afer the split.

 - 2018-12-23: `flock.mk`: Introduce.

 - 2018-12-22: `go-mod.mk`: Enable parallel builds for Go 1.12+
 - 2018-12-22: `go-workspace.mk`: Enable parallel builds for Go 1.10+
 - 2018-12-22: Almost exclusively use `$(KUBECONFIG)` to refer to `cluster.knaut`.  This allows the
               caller to override the value by passing a `KUBECONFIG=…` argument to `make`.  This is
               an important property for running `make check` inside of `testbench`, so that
               testbench can provide the Kubernaut file.  The exception is that we still hard-code
               the value in `clean: cluster.knaut.clean` so that `make clean` doesn't remove the
               user's Kubernaut file.

 - 2018-12-22: `_go-common.mk`: Add a variable to disable `go-test`'s automatic definition

 - 2018-12-20: BREAKING CHANGE: `common.mk`: The argument order of `$(call joinlist,…)` has changed.

 - 2018-12-20: BREAKING CHANGE: `go-{mod,workspace}.mk`: `$(pkg)` is now `$(go.module)`, and is no
               longer necessary to manually set; `go-{mod,workspace}.mk` are both smart enough to
               set it automatically!
 - 2018-12-20: BREAKING CHANGE: `go-{mod,workspace}.mk`: `$(bins)` is now `$(notdir $(go.bins))`,
               and is no longer nescessary to manually set; `go-{mod,workspace}.mk` are both smart
               enough to set it automatically!
 - 2018-12-20: BREAKING CHANGE: `go-{mod,workspace}.mk`: Go binaries now go in
               `./bin_$(GOOS)_$(GOARCH)/` by default, instead of the old default of `./`
 - 2018-12-20: `go-{mod,workspace}.mk`: The `Makefile` doesn't need to export `GOPATH` or anything
               like that, the .mk snippets now export things.
 - 2018-12-20: `go-{mod,workspace}.mk`: It's no longer necessary to use `$(GO)`, just write
               `go`--but don't don't use it inside of `$(shell …)` if you can avoid it, exported
               variables don't affect `$(shell …)`.
 - 2018-12-20: `common.mk`: It's no longer necessary to clean up `bin_*/`, in your `clean:` rule,
               `common.mk` takes care of that for you.
 - 2018-12-20: `common.mk` goes ahead and declares several common targets as `.PHONY`

 - 2018-12-06: Rename `go.mk` to `go-workspace.mk`, introduce a new `go-mod.mk`.
