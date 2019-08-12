# Hacking on build-aux.git

## Misc notes

 - If you have a dependency on another `.mk` file includes, include it
   with `include $(dir $(lastword $(MAKEFILE_LIST)))common.mk`.
 - `.PHONY` targets that you wish to be user-visible should have a `##
   Help text` usage comment.  See `help.mk` for more information.
 - Wrap your `.mk` files in

		ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
		…
		endif

   include guards to make sure they are only included once; similar to
   how you would with a C header file.
 - The Make `export` directive *only* affects recipes, and does *not*
   affect `$(shell …)`.  Because of this, you shoud not call `go`
   inside of `$(shell …)`, as `$GO111MODULE` may not have the correct
   value.
 - Make sure to pass `--fail` to `curl` when downloading things;
   otherwise it will silently save 404 HTML/XML pages.
 - Don't depend on anything in ./build-aux/bin/ during clean and
   clobber rules.  The `prelude.mk` cleanup might remove it before
   your cleanup bit runs.  For Go programs, `cd` to the program's
   sourcedirectory, and use `go run .` to run it:

		cd $(dir $(_myfile.mk))bin-go/PROGRAM && GO111MODULE=on go run . ARGS...

   Only tolerate that grossness in your cleanup rules.

## Naming conventions

 - `check` and `check-FOO` are `.PHONY` targets that run tests.
 - `test-FOO` is an executable program that when run tests FOO.
   Perhaps the `check-FOO` Make target compiles and runs the
   `test-FOO` program.
 - `test` is the POSIX `test(1)` command.  Don't use it as a Makefile
   rule name.
 - (That is, use "check" as a *verb*, and "test" as a *noun*.)
 - Internal "private" variables should be named `_snippet-name.VAR`;
   for example, a variable internal to `k8s.mk` might be named
   `_k8s.push`.

## Compatibility

 - Everything should work with GNU Make 3.81 AND newer versions.
   * Avoid Make features introduced in 3.82 or later.
   * The 3.81→3.82 update changed precedence of pattern rules from
     "parse-order-based" to "stem-length-based".  Be careful that your
     pattern rules work in BOTH systems.
 - Requires `go` 1.11 or newer.
 - Using `--` to separate positional arguments isn't POSIX, but is
   implemented in `getopt(3)` and `getopt_long(3)` in every major libc
   (including macOS).  Therefore, `--` working is a reasonable base
   assumption.  Known exceptions:
    * macOS `chmod`
 - Prefer `curl` to `wget`; macOS ships with `curl`, it doesn't ship
   with `wget`.

## Style guide

 - See "Naming conventions"..
 - See [`docs/conventions.md`](./docs/conventions.md).
 - Place `.PHONY:` immediately *after* the rule definition.
 - Use pattern rules instead of "old-fashioned suffix rules" (as the
   GNU Make manual refers to them).
