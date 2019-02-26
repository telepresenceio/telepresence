# Datawire build-aux Go language support

## Workspaces vs Modules

Currently, there are 2 options for Go projects:

 - `go-workspace.mk`: For GOPATH workspaces
 - `go-mod.mk`: For Go 1.11 modules

Besides the differences outlined in this section, `go-workspace.mk`
and `go-mod.mk` should work more-or-less identically.

### Initializing a `go-workspace.mk` project

	$ MODULE=EXAMPLE.COM/YOU/GITREPO

	$ echo 'include build-aux/go-workspace.mk' >> Makefile
	$ echo /.go-workpsace >> .gitignore
	$ echo "!/.go-workspace/src/${MODULE}" >> .gitignore
	$ mkdir -p $(dirname .go-workspace/src/${MODULE})
	$ ln -s $(dirname .go-workspace/src/${MODULE} | sed -E 's,[^/]+,..,g') .go-workspace/src/${MODULE}

What's that big expression in the `ln -s` command!?  It's the same as

	$ ln -sr . .go-workspace/src/${MODULE}

but for lame operating systems that ship an `ln` that doesn't
understand the `-r` flag.

### Initializing a `go-mod.mk` project

	$ go mod init EXAMPLE.COM/YOU/GITREPO

	$ echo 'include build-aux/go-mod.mk' >> Makefile

### Migrating from `go-workspace.mk` to `go-mod.mk`

	$ go mod init EXAMPLE.COM/YOU/GITREPO

	$ make clobber
	$ rm -rf -- .go-workspace vendor glide.* Gopkg.*
	$ sed -i -E 's,/go-workspace\.mk,/go-mod.mk,' Makefile
	$ sed -i -e '/\.go-workspace/d' .gitignore

## Using `go-{mod,workspace}.mk`

`go-{mod,workspace}.mk` try hard to automatically find whatever source
files you write, and do the right thing for each of the standard
`common.mk` targets.  By default:

 - `build`: Any `package "main"` package will automatically get build,
   and placed in `./bin_$(GOOS)_$(GOARCH)` (assuming the `+build` tags
   pass).  If you have any helper scripts that you intend to run with
   `go run`, and don't want to be compiled, you should put `// +build
   ignore` in them.
 - `check`: All packages in your project are automatically tested with `go test`.
 - `lint`: All packages in your project are automatically linted with
   `go vet`, and are verified to be formatted with `gofmt -s`.
 - When using `go-workspace.mk`, the `vendor` directory is created
   automatically, using either Glide (if `glide.yaml` exists), or
   `dep` (if `Gopkg.toml` exists).

Knobs you can turn in your `Makefile`:

 - `go.PLATFORMS`: A list of `GOOS_GOARCH` pairs to compile
   executables for.  Just `$(go env GOOS)_$(go env GOARCH)` by default
   (that is, the host platform, unless you've set the `GOOS` or
   `GOARCH` environment variables).
 - `go.DISABLE_GO_TEST`: If set to a non-empty string, don't have
   `make check` run `go test`.  This is useful if you need to pass
   special flags to `go test`, and would like to write the rule
   yourself.
 - `make lint` behavior can be modified either by creating a
   `.golangci.yml`, `.golangci.toml`, `.golangci.json` file; or by
   setting `go.GOLANG_LINT_FLAGS`.  See the [`golangci-lint`
   docs](https://github.com/golangci/golangci-lint) for information on
   that file format, and what valid flags are.
 - `go.LDFLAGS` can specify any flags to pass to the Go linker.
   `go-version.mk` uses this to set the `main.Version` symbol.

## `go-version.mk`

If you `include build-aux/go-version.mk`, it will set `go.LDFLAGS` to
include a `main.Version` symbol, which a string set to the
`$(VERSION)` Make variable

	go.LDFLAGS += -X main.Version=$(VERSION)

If you do not set `VERSION` in your `Makefile`, it will be set
automatically by [`version.mk`](../version.mk).
