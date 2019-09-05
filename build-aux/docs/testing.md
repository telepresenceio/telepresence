# Datawire build-aux Test Harness

`common.mk` includes a built-in test harness that allows you to plug
in your own tests, and have them aggregated and summarized, like:

	$ make check
	...
	PASS: go-test 5 - github.com/datawire/apro/cmd/amb-sidecar.TestAppNoToken
	PASS: go-test 6 - github.com/datawire/apro/cmd/amb-sidecar.TestAppBadToken
	PASS: go-test 7 - github.com/datawire/apro/cmd/amb-sidecar.TestAppBadCookie
	PASS: go-test 8 - github.com/datawire/apro/cmd/amb-sidecar.TestAppCallback
	PASS: go-test 9 - github.com/datawire/apro/cmd/amb-sidecar.TestAppCallbackNoCode
	PASS: tests/local/apictl.tap.gen 1 - check_version
	============================================================================
	test-suite summary
	============================================================================
	# TOTAL:  10
	# SKIP:    0
	# PASS:   10
	# XFAIL:   0
	# FAIL:    0
	# XPASS:   0
	# ERROR:   0
	============================================================================
	make[1]: Leaving directory '/home/lukeshu/src/apro'

(If you were viewing that in a terminal, it would also be pretty and
colorized.)

Each test-case can have one of 6 results:

 - `pass`, `fail`, `skip`: You can guess what these mean.
 - `xfail` ("expected fail"): The test failed, but it was expected to
   fail.  Perhaps you wrote a test for a feature for before
   implementing the feature.  Perhaps you wrote a test that captures a
   bug report, but haven't written the fix yet.  Makes
   Test-Driven-Development possible.
 - `xpass` ("unexpected pass"): The test passed, but it was expected
   to xfail.  This either means you did a poor job implementing the
   test, and it doesn't really check what you think it checks, or it
   means you've implemented the fix, but forgot to replace `xfail`
   with `fail` in the test.
 - `error`: It isn't that the test decided that there's bug in the
   code-under-test, it's that the test itself encountered an error.

You can use any test framework or language you like, as long as it can
emit [TAP, the Test Anything Protocol][TAP] (or something that can be
translated to TAP).  Both TAP version 12 and version 13 are supported.
`not ok # TODO` is "xfail", while , `ok # TODO` is "xpass"

 > Side-Note: pytest-tap emits `ok # TODO` for both xfail and xpass,
 > which is wrong, and I consider to be a bug in pytest-tap.

## Built-in test runners

By default, `common.mk` knows how to run test cases of 2 types (but
you can easily add more):

 - `*.test` GNU Automake-compatible standalone test cases.  One file
   is one test case.  `FOO.test` must be an executable file.  It is
   run with no arguments, stdout and stderr are ignored (but logged to
   `FOO.log`); it is the exit code that determines the test result:

    * 0 => pass
	* 77 => skip
	* anything else => fail

   It is not possible to xfail or xpass with this type of test.

 - `*.tap.gen` TAP-emitting test cases.  You may have multiple test
   cases per file.  `FOO.tap.gen` must be an executable file.  It is
   run with no arguments; stdout and stderr are merged, and are taken
   to be a TAP stream (both v12 and v13 are supported).  The exit code
   is ignored.

`common.mk` does *not* scan your source directory for tests (but
`go-*.mk` will scan for `go test` tests).  In your `Makefile` You must
explicitly tell it about any `.test` or `.tap.gen` files that you
would like it to include in `make check`.  For example, if you would
like it to include any `.test` or `.tap.gen` files in the `./tests/`
directory, you could write:

	test-suite.tap: $(patsubst %.test,%.tap,$(wildcard tests/*.test))
	test-suite.tap: $(patsubst %.tap.gen,%.tap,$(wildcard tests/*.tap.gen))

## Adding your own test runners

To add a new test runner, you just need a command that emits TAP:
`tee` it to a `.tap` file, and pipe that to `$(TAP_DRIVER) stream -n
TEST_GROUP_NAME` to pretty-print the results as they happen:

	# Tell Make how to run the test command, and stream the results to
	# `$(TAP_DRIVER) stream` to pretty-print the results as they happen.
	my-test.tap: my-test.input $(TAP_DRIVER) FORCE
		@SOME_COMMAND_THAT_EMITS_TAP 2>&1 | tee $@ | $(TAP_DRIVER) stream -n my-test

	# Tell Make to include 'my-test' in `make check`
	test-suite.tap: my-test.tap

For example, to use [BATS (Bash Automated Testing System)][BATS], you
would write:

	%.tap: %.bats $(TAP_DRIVER) FORCE
		@bats --tap $< | tee $@ | $(TAP_DRIVER) stream -n $<

	# Automatically include `./tests/*.bats`
	test-suite.tap: $(patsubst %.bats,%.tap,$(wildcard tests/*.bats))

If your test framework of choice doesn't support TAP output, you can
pipe it to a helper program that can translate it.  For example, `go
test` doesn't support TAP output, but `go test -json` output is
parsable, so we pipe that to [gotest2tap][gotest2tap], which
translates it to TAP.

If you set `SHELL = sh -o pipefail` in your `Makefile` (the pros and
cons of which I won't comment on here), you should be sure that if
your test-runner indicates success or failure with an exit code, that
you ignore that exit code:

	%.tap: %.bats $(TAP_DRIVER) FORCE
		@{ bats --tap $< || true; } | tee $@ | $(TAP_DRIVER) stream -n $<

## Adding dependencies of tests

It is reasonassumed that *all* tests depend on `make build`.  To add a
dependency shared by all tests, to declare a dependency that all tests
should depend on, declare it as a dependency of `check` itself.  For
example, `common.mk` says:

	check: lint build

As another example, the `Makefile` for Ambassador Pro says:

	check: $(if $(HAVE_DOCKER),deploy proxy)

To declare a dependency for an individual test is a little trickier,
because you must keep in mind what type of test it is.  For `.tap.gen`
tests, you must declare it both on the `.tap` file and (if the
dependency is not a `.tap`) on `check` itself:

    test-suite.tap: tests/cluster/oauth-e2e.tap
	check tests/cluster/oauth-e2e.tap: tests/cluster/oauth-e2e/node_modules

If that were a `.test` test, you would need to declare it on the
`.log` file instead of `.tap`:

    test-suite.tap: tests/cluster/oauth-e2e.tap
	check tests/cluster/oauth-e2e.log: tests/cluster/oauth-e2e/node_modules

If you need one test to depend on another test, write the dependency
using the `.tap` suffix (not `.log`).  You do not need to write the
depenency for `check`, since it will already depend on the `.tap`
through `test-suite.tap`:

    test-suite.tap: foo.tap bar.tap
    foo.log: bar.tap

[TAP]: https://testanything.org
[BATS]: https://github.com/sstephenson/bats
[gotest2tap]: ../bin-go/gotest2tap/
