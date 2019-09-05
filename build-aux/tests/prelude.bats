#!/usr/bin/env bats

load common

@test "prelude.mk: joinlist with separator" {
	check_expr_eq echo '$(call joinlist,/,foo bar baz)' 'foo/bar/baz'
}

@test "prelude.mk: joinlist without separator" {
	check_expr_eq echo '$(call joinlist,,foo bar baz)' 'foobarbaz'
}

@test "prelude.mk: quote.shell" {
	# This test relies on the fact that 'var.mk' is implemented
	# using `quote.shell`.
	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		include build-aux/var.mk
		define actual
		some'string"with`special characters)
		and newlines	and tabs
		and 2 trailing newlines


		endef
		all: $(var.)actual
	__EOT__

	make
	printf 'some'\''string"with`special characters)\nand newlines\tand tabs\nand 2 trailing newlines\n\n' > expected
	diff -u expected build-aux/.var.actual
}

@test "prelude.mk: lazyonce" {
	if [[ "$(make --version | head -n1)" == 'GNU Make 3.81' ]]; then
		skip
	fi
	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		var = $(call lazyonce,var,$(info eval-time)value)
		$(info before)
		$(info a: $(var))
		$(info b: $(var))
		$(info c: $(var))
		all: noop
		noop: ; @true
		.PHONY: noop
	__EOT__

	make > actual
	printf '%s\n' > expected \
	       'before' \
	       'eval-time' \
	       'a: value' \
	       'b: value' \
	       'c: value'
	diff -u expected actual
}

@test "prelude.mk: build-aux.dir" {
	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		include build-aux/var.mk
		all: $(var.)build-aux.dir
	__EOT__

	make
	# Check that it points to the right place
	[[ "$(cat build-aux/.var.build-aux.dir)" -ef build-aux ]]
}

@test "prelude.mk: build-aux.bindir" {
	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		include build-aux/var.mk
		all: $(build-aux.bindir) $(var.)build-aux.bindir
	__EOT__

	make
	# Check that it points to the right place
	[[ "$(cat build-aux/.var.build-aux.bindir)" -ef build-aux/bin ]]
	# Check that it's absolute
	[[ "$(cat build-aux/.var.build-aux.bindir)" == /* ]]
}

@test "prelude.mk: FLOCK" {
	if type flock &>/dev/null; then
		check_executable prelude.mk FLOCK
	else
		check_go_executable prelude.mk FLOCK
		[[ "$FLOCK" != unsupported ]] || return 0
	fi
	if which flock &>/dev/null; then
		[[ "$FLOCK" == "$(which flock)" ]]
	fi
	if [[ -n "$build_aux_expected_FLOCK" ]]; then
		[[ "$FLOCK" == "$build_aux_expected_FLOCK" ]]
	fi

	# TODO: Check that $FLOCK behaves correctly
}

@test "prelude.mk: COPY_IFCHANGED" {
	check_executable prelude.mk COPY_IFCHANGED

	# TODO: Check that $COPY_IFCHANGED behaves correctly
}

@test "prelude.mk: MOVE_IFCHANGED" {
	check_executable prelude.mk MOVE_IFCHANGED

	# TODO: Check that $MOVE_IFCHANGED behaves correctly
}

@test "prelude.mk: WRITE_IFCHANGED" {
	check_executable prelude.mk WRITE_IFCHANGED

	# TODO: Check that $WRITE_IFCHANGED behaves correctly
}

@test "prelude.mk: TAP_DRIVER" {
	check_executable prelude.mk TAP_DRIVER

	# TODO: Check that $TAP_DRIVER behaves correctly
}

@test "prelude.mk: clobber" {
	if ! [[ -e build-aux/.git ]]; then
		# Because we check `git clean -ndx` to make sure
		# things are clean.
		skip
	fi
	# Let's not work with a symlink for this test
	rm "$test_tmpdir/build-aux"
	cp -a "$BATS_TEST_DIRNAME/.." "$test_tmpdir/build-aux"
	(cd build-aux && git clean -fdx)

	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		include build-aux/var.mk
		all: $(COPY_IFCHANGED) $(MOVE_IFCHANGED) $(WRITE_IFCHANGED) $(TAP_DRIVER)
	__EOT__

	[[ -d build-aux ]]
	[[ ! -d build-aux/bin ]]
	make all
	[[ -d build-aux/bin ]]
	[[ -f build-aux/bin/copy-ifchanged && -x build-aux/bin/copy-ifchanged ]]
	[[ -n "$(cd build-aux && git clean -ndx)" ]]
	make clobber
	[[ -d build-aux ]]
	[[ ! -d build-aux/bin ]]
	[[ -z "$(cd build-aux && git clean -ndx)" ]]
}

@test "prelude.mk: build-aux.bin-go.rule" {
	# TODO
}

@test "prelude.mk: FORCE" {
	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		all: without-force with-force
		without-force: ; touch $@
		with-force: FORCE ; touch $@
	__EOT__

	make
	cp -a with-force with-force.bak
	cp -a without-force without-force.bak

	sleep 2

	make
	ls -l
	[[ with-force -nt with-force.bak ]]
	[[ ! without-force -nt without-force.bak ]]
	[[ ! without-force -ot without-force.bak ]]
}
