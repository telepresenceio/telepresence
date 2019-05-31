# Datawire build-aux

This is a collection of Makefile snippets (and associated utilities)
for use in Datawire projects.

It has the following downsides:
 1. It does not support out-of-tree builds.
 2. It has no notion of nesting.  You cannot `cd` to a sub-directory,
    run `make`, and have it just build the stuff in that directory.
 3. It has no notion of nesting.  If you want per-directory build
    descriptions, you'll have to build that functionality yourself, or
    offload it to a separate build-system, like `go` or `setup.py`.
 4. Most of the `.mk` snippets have a hard dependency on the `go`
    program being in `PATH`, even if none of the sources are Go.

If any of those are a bummer, but you still want the "Make snippets in
a `./build-aux/` folder" concept, consider
[Autothing](https://git.lukeshu.com/autothing/) (which has the
downsides that it requires GNU Make 3.82 or above, and is slow).

At Datawire, those are good trade-offs, since:
 - We're mostly a Python and Go shop (we don't care about #2 or #3,
   and only care about #4 for Python-only projects).
 - I've never heard anyone here even mention out-of-tree builds (#1).
 - We need to support macOS (which still ships GNU Make 3.81, as it's
   the last GPLv2 version) (so Autothing is ruled out).

## How to use

Add `build-aux.git` as `build-aux/` in the git repository of the
project that you want to use this from.  I recommend that you do this
using `git subtree`, but `git submodule` is fine too.

Then, in your Makefile, write `include build-aux/FOO.mk` for each
common bit of functionality that you want to make use of.

### Using `git-subtree` to manage `./build-aux/`

 - Start using build-aux:

		$ git subtree add --squash --prefix=build-aux git@github.com:datawire/build-aux.git master

 - Update to latest build-aux:

		$ ./build-aux/build-aux-pull

 - Push "vendored" changes upstream to build-aux.git:

		$ ./build-aux/build-aux-push

### Documentation

 - Each `.mk` snippet contains a reference-quality header comment
   identifying
    - any "eager" inputs (mostly variables); these must be defined
      *before* including the `.mk` file.
    - any "lazy" inputs (mostly variables); these may be defined
      before or after including the `.mk` file.
    - any outputs (targets, variables)
    - which targets from other snippets it hooks in to (mostly hooking
      in to `common.mk` targets)
 - [`docs/intro.md`](./docs/intro.md) is an introduction to
   big-picture ideas in `*.mk` snippets.
 - [`docs/golang.md`](./docs/golang.md) discusses support for building
   software written in Go.
 - [`docs/testing.md`](./docs/testing.md) discusses adding tests to
   `make check`.
 - [`HACKING.md`](./HACKING.md) has guidelines for contributing to
   `build-aux.git`.
