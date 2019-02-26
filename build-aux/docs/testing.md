# Datawire build-aux Test Harness

`common.mk` includes a built-in test harness that allows you to plug
in your own tests, and have them aggregated and summarized, like:

	$ make check
	...
	PASS: go-test 5 -  - TestAppCallbackNoCode (0.00s)
	PASS: go-test 6 -  - TestNewAgent_AmbassadorIDToConsulServiceName (0.00s)
	PASS: go-test 7 -  - TestNewAgent_SecretName (0.00s)
	PASS: go-test 8 -  - TestFormatKubernetesSecretYAML (0.00s)
	PASS: go-test 9 -  - TestCreateCertificateChain (0.00s)
	============================================================================
	test-suite summary
	============================================================================
	# TOTAL:  13
	# SKIP:    0
	# PASS:   13
	# XFAIL:   0
	# FAIL:    0
	# XPASS:   0
	# ERROR:   0
	============================================================================

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

To add a new test runner, you just need a command that emits TAP:,
`tee` it to a `.tap` file, and pipe that to `build-aux/tap-driver
stream -n TEST_GROUP_NAME` to pretty-print the results as they happen:

	# Tell Make how to run the test command, and stream the results to
	# `tap-driver stream` to pretty-print the results as they happen.
	my-test.tap: my-test.input build FORCE
		SOME_COMMAND_THAT_EMITS_TAP 2>&1 | tee $@ | build-aux/tap-driver stream -n my-test

	# Tell Make to include 'my-test' in `make check`
	test-suite.tap: my-test.tap

For example, to use [BATS (Bash Automated Testing System)][BATS], you
would write:

	%.tap: %.bats build FORCE
		bats --tap $< | tee $@ | build-aux/tap-driver stream -n $<

	# Automatically include `./tests/*.bats`
	test-suite.tap: $(patsubst %.bats,%.tap,$(wildcard tests/*.bats))

If your test framework of choice doesn't support TAP output, you can
pipe it to a helper program that can translate it.  For example, `go
test` doesn't support TAP output, but `go test -v` output is parsable,
so we pipe that to [patter][patter], which translates it to TAP.

[TAP]: https://testanything.org
[BATS]: https://github.com/sstephenson/bats
[patter]: https://github.com/apg/patter
