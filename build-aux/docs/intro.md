# Datawire build-aux Introduction

build-aux is a collection of Makefile `.mk` snippets (and associated
utilities).  Each of the `.mk` snippets is written to be as
independent from the others as possible, but integrate well by
following common conventions.  You may pick-and-choose which to
include simply by writing `include build-aux/NAME.mk` in your
`Makefile`.

Each `.mk` snippet starts with a reference header-comment
identifying:

 - any eager-evaluated inputs (mostly variables)
 - any lazy-evaluated inputs (mostly variables)
 - any outputs (targets, variables)
 - which targets from other snippets it hooks in to (mostly hooking
   in to `common.mk` targets)

Eager inputs need to be defined *before* loading the snippet; lazy
inputs can be defined later.  See [`conventions.mk`][./conventions.md]
for more information on the reference header-comment.

For the most part, you don't need to worry about dependencies between
`.mk` files; each file will automatically `include` the others it
depends on.  However, if you would like to use an output from one
snippet as an eager input to another, then you do need to worry about
include order; if you would like to use `kubernaut-ui.mk` to set
`KUBECONFIG` for `teleproxy.mk`, then you will need to make sure you
include `kubernaut-ui.mk` *before* you include `teleproxy.mk`.  You
don't need to worry about including a file twice; this is safe, as
they all have C-header-style include guards.

## `common.mk`

Most (but not all) of the snippets include `common.mk`, which

 1. Configures Make to use sane settings (since the default settings
    are set for historical compatibility, not the "right thing").

 2. Defines several `$(call …)`-able helper functions, like
    `joinlist`, `path.trimprefix`, or `quote.shell`.  See
    [`common.mk`](../common.mk) itself for the full list, and
    documentation on how to use them.

 3. Declares several utility variables:
    - `GOOS`, `GOARCH`: Operating system name, and CPU architecture.
      `GO` is included in the variable names to indicate that they use
      the same names as `go env` (as opposed to using `uname
      -s`/`uname -m` names, or something else).  Most of the time,
      these are the host OS or architecture.  But, when inside of
      recipes for files in `bin_OS_ARCH/` directories, they are set to
      that OS and architecture.
    - `NL`: a single newline character, since that's hard to type in Make
    - `SPACE`: a single space character, since that's hard to type in
      Make

 4. Declares several high-level common targets, like `make build`,
    `make clean`, or `make check`.  See [`common.mk`](../common.mk)
    itself for the full list.  By defining a list of common
    user-facing targets, the rest of the snippets can hook in to that
    and provide a uniform interface.  With the exception of `make
    check` (which is special, see below), your `Makefile` can extend
    any of these by adding dependencies to the target name.  For
    example, to have `make build` build the `foo` file, you could
    write:

		build: foo

    or to have `make lint` run the `flake8` Python linter, you could
    write

		lint: flake8
		flake8:
			flake8 mypackage/
		.PHONY: flake8

    or

		lint:
			flake8 mypackage/

    The former has the advantage that it allows you to add multiple
	hooks on to `lint` (so it's the only method that build-aux
	snippets use), and also allows flake8 to run in parallel with
	other linters set up by build-aux snippets.

    `make check` is special in how it works.  Instead of writing

		check: my-test                  # WRONG

    you write

		test-suite.tap: my-test.tap     # CORRECT

    See [`testing.md`](./testing.md) for information about writing the
    recipe for `my-test.tap`.

    Because of the special semantics around `$(GOOS)` and `$(GOARCH)`
    in `bin_OS_ARCH/` directories, `common.mk` goes ahead and has
    `make clean` run `rm -rf -- bin_*`.  Because of `make check`'s use
    of `test-suite.tap`, `common.mk` also goes head and has `make
    clean` run `rm -f test-suite.tap`.

    With the exceptions of `make check` and `make clean`, `common.mk`
    only provides empty definitions; it is up to your `Makefile`, or
    other `.mk` snippets to make these rules actually do something.

## `help.mk`

The snippet `help.mk` adds a `make help` target, that display
information about your project (customizable by setting the
`help.body` variable from your `Makefile`), and a table of all of the
`make FOO` targets that Make knows about.  Any targets you write in
your `Makefile` that you think should be visible can be added to this
listing by writing a magic comment:

	frob: ## Frobnicate the splorks

See [`help.mk`](../help.mk) itself for full documentation.
