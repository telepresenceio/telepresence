#!/usr/bin/env bats

load common

truncated_diff() {
	if ! diff "$@" > diff.patch; then
		head diff.patch
		false
	fi
}

@test "prelude_go.mk: GOHOSTOS" {
	[[ -n "$build_aux_expeced_GOHOSTOS" ]] || skip
	check_expr_eq strict '$(GOHOSTOS)' "$build_aux_expeced_GOHOSTOS"
}

@test "prelude_go.mk: GOHOSTARCH" {
	[[ -n "$build_aux_expeced_GOHOSTARCH" ]] || skip
	check_expr_eq strict '$(GOHOSTARCH)' "$build_aux_expeced_GOHOSTARCH"
}

@test "prelude_go.mk: _prelude.go.lock" {
	# TODO
}

@test "prelude_go.mk: _prelude.go.VERSION.parse" {
	mkdir expected
	testcase() {
		[[ $# = 2 ]]
		echo $2 > "expected/$1"
	}
	# Let's go ahead and test this on every version that's been
	# released so far:
	#
	# Generate that list from go.git:
	#    git tag|sed -n '/^go[0-9.]*$/s/^go//p'|sort -V|while read -r rel; do git tag|sed -n "/^go${rel}[a-z]/s/^go//p"; echo $rel; done
	# and manually put in the expected output.
	testcase '1'         '1  0  0'
	testcase '1.0.1'     '1  0  1'
	testcase '1.0.2'     '1  0  2'
	testcase '1.0.3'     '1  0  3'
	testcase '1.1rc2'    '1  1  0 rc2'
	testcase '1.1rc3'    '1  1  0 rc3'
	testcase '1.1'       '1  1  0'
	testcase '1.1.1'     '1  1  1'
	testcase '1.1.2'     '1  1  2'
	testcase '1.2rc2'    '1  2  0 rc2'
	testcase '1.2rc3'    '1  2  0 rc3'
	testcase '1.2rc4'    '1  2  0 rc4'
	testcase '1.2rc5'    '1  2  0 rc5'
	testcase '1.2'       '1  2  0'
	testcase '1.2.1'     '1  2  1'
	testcase '1.2.2'     '1  2  2'
	testcase '1.3beta1'  '1  3  0 beta1'
	testcase '1.3beta2'  '1  3  0 beta2'
	testcase '1.3rc1'    '1  3  0 rc1'
	testcase '1.3rc2'    '1  3  0 rc2'
	testcase '1.3'       '1  3  0'
	testcase '1.3.1'     '1  3  1'
	testcase '1.3.2'     '1  3  2'
	testcase '1.3.3'     '1  3  3'
	testcase '1.4beta1'  '1  4  0 beta1'
	testcase '1.4rc1'    '1  4  0 rc1'
	testcase '1.4rc2'    '1  4  0 rc2'
	testcase '1.4'       '1  4  0'
	testcase '1.4.1'     '1  4  1'
	testcase '1.4.2'     '1  4  2'
	testcase '1.4.3'     '1  4  3'
	testcase '1.5beta1'  '1  5  0 beta1'
	testcase '1.5beta2'  '1  5  0 beta2'
	testcase '1.5beta3'  '1  5  0 beta3'
	testcase '1.5rc1'    '1  5  0 rc1'
	testcase '1.5'       '1  5  0'
	testcase '1.5.1'     '1  5  1'
	testcase '1.5.2'     '1  5  2'
	testcase '1.5.3'     '1  5  3'
	testcase '1.5.4'     '1  5  4'
	testcase '1.6beta1'  '1  6  0 beta1'
	testcase '1.6beta2'  '1  6  0 beta2'
	testcase '1.6rc1'    '1  6  0 rc1'
	testcase '1.6rc2'    '1  6  0 rc2'
	testcase '1.6'       '1  6  0'
	testcase '1.6.1'     '1  6  1'
	testcase '1.6.2'     '1  6  2'
	testcase '1.6.3'     '1  6  3'
	testcase '1.6.4'     '1  6  4'
	testcase '1.7beta1'  '1  7  0 beta1'
	testcase '1.7beta2'  '1  7  0 beta2'
	testcase '1.7rc1'    '1  7  0 rc1'
	testcase '1.7rc2'    '1  7  0 rc2'
	testcase '1.7rc3'    '1  7  0 rc3'
	testcase '1.7rc4'    '1  7  0 rc4'
	testcase '1.7rc5'    '1  7  0 rc5'
	testcase '1.7rc6'    '1  7  0 rc6'
	testcase '1.7'       '1  7  0'
	testcase '1.7.1'     '1  7  1'
	testcase '1.7.2'     '1  7  2'
	testcase '1.7.3'     '1  7  3'
	testcase '1.7.4'     '1  7  4'
	testcase '1.7.5'     '1  7  5'
	testcase '1.7.6'     '1  7  6'
	testcase '1.8beta1'  '1  8  0 beta1'
	testcase '1.8beta2'  '1  8  0 beta2'
	testcase '1.8rc1'    '1  8  0 rc1'
	testcase '1.8rc2'    '1  8  0 rc2'
	testcase '1.8rc3'    '1  8  0 rc3'
	testcase '1.8'       '1  8  0'
	testcase '1.8.1'     '1  8  1'
	testcase '1.8.2'     '1  8  2'
	testcase '1.8.3'     '1  8  3'
	testcase '1.8.4'     '1  8  4'
	testcase '1.8.5rc4'  '1  8  5 rc4'
	testcase '1.8.5rc5'  '1  8  5 rc5'
	testcase '1.8.5'     '1  8  5'
	testcase '1.8.6'     '1  8  6'
	testcase '1.8.7'     '1  8  7'
	testcase '1.9beta1'  '1  9  0 beta1'
	testcase '1.9beta2'  '1  9  0 beta2'
	testcase '1.9rc1'    '1  9  0 rc1'
	testcase '1.9rc2'    '1  9  0 rc2'
	testcase '1.9'       '1  9  0'
	testcase '1.9.1'     '1  9  1'
	testcase '1.9.2'     '1  9  2'
	testcase '1.9.3'     '1  9  3'
	testcase '1.9.4'     '1  9  4'
	testcase '1.9.5'     '1  9  5'
	testcase '1.9.6'     '1  9  6'
	testcase '1.9.7'     '1  9  7'
	testcase '1.10beta1' '1 10  0 beta1'
	testcase '1.10beta2' '1 10  0 beta2'
	testcase '1.10rc1'   '1 10  0 rc1'
	testcase '1.10rc2'   '1 10  0 rc2'
	testcase '1.10'      '1 10  0'
	testcase '1.10.1'    '1 10  1'
	testcase '1.10.2'    '1 10  2'
	testcase '1.10.3'    '1 10  3'
	testcase '1.10.4'    '1 10  4'
	testcase '1.10.5'    '1 10  5'
	testcase '1.10.6'    '1 10  6'
	testcase '1.10.7'    '1 10  7'
	testcase '1.10.8'    '1 10  8'
	testcase '1.11beta1' '1 11  0 beta1'
	testcase '1.11beta2' '1 11  0 beta2'
	testcase '1.11beta3' '1 11  0 beta3'
	testcase '1.11rc1'   '1 11  0 rc1'
	testcase '1.11rc2'   '1 11  0 rc2'
	testcase '1.11'      '1 11  0 '
	testcase '1.11.1'    '1 11  1'
	testcase '1.11.2'    '1 11  2'
	testcase '1.11.3'    '1 11  3'
	testcase '1.11.4'    '1 11  4'
	testcase '1.11.5'    '1 11  5'
	testcase '1.11.6'    '1 11  6'
	testcase '1.11.7'    '1 11  7'
	testcase '1.11.8'    '1 11  8'
	testcase '1.11.9'    '1 11  9'
	testcase '1.11.10'   '1 11 10'
	testcase '1.11.11'   '1 11 11'
	testcase '1.11.12'   '1 11 12'
	testcase '1.11.13'   '1 11 13'
	testcase '1.12beta1' '1 12  0 beta1'
	testcase '1.12beta2' '1 12  0 beta2'
	testcase '1.12rc1'   '1 12  0 rc1'
	testcase '1.12'      '1 12  0'
	testcase '1.12.1'    '1 12  1'
	testcase '1.12.2'    '1 12  2'
	testcase '1.12.3'    '1 12  3'
	testcase '1.12.4'    '1 12  4'
	testcase '1.12.5'    '1 12  5'
	testcase '1.12.6'    '1 12  6'
	testcase '1.12.7'    '1 12  7'
	testcase '1.12.8'    '1 12  8'
	testcase '1.12.9'    '1 12  9'

	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		all: $(patsubst expected/%,actual/%,$(wildcard expected/*))
		actual/%: ; @echo $(call _prelude.go.VERSION.parse,$*) > $@
	__EOT__

	mkdir actual
	make -j128
	truncated_diff -ruN expected actual
}

@test "prelude_go.mk: _prelude.go.VERSION.prerelease.ge" {
	local versions=(
		# From go.git:
		#     git tag|sed -n 's/^go[0-9.]*//p'|sort -u
		beta1
		beta2
		beta3
		rc1
		rc2
		rc3
		rc4
		rc5
		rc6
	)
	mkdir expected
	local a b expect
	for a in "${versions[@]}"; do
		expect=true
		for b in "${versions[@]}"; do
			echo $expect > "expected/$a-ge-$b"
			# early on, 'b' will be low, so a>=b is true;
			# once 'b' catches 'a', that will flip to
			# false.
			if [[ "$a" == "$b" ]]; then
				expect=false
			fi
		done
	done

	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		all: $(patsubst expected/%,actual/%,$(wildcard expected/*))
		A = $(word 1,$(subst -ge-, ,$*))
		B = $(word 2,$(subst -ge-, ,$*))
		actual/%: ; @echo $(if $(call _prelude.go.VERSION.prerelease.ge,$A,$B),true,false) > $@
	__EOT__

	mkdir actual
	make -j128
	truncated_diff -ruN expected actual
}

@test "prelude_go.mk: _prelude.go.VERSION.HAVE" {
	local versions=(
		# From go.git:
		#    git tag|sed -n '/^go[0-9.]*$/s/^go//p'|sort -V|while read -r rel; do git tag|sed -n "/^go${rel}[a-z]/s/^go//p"; echo $rel; done
		#
		# Comment out uninteresting parts in the middle,
		# because (136 versions)^2 = 18_496 testcases, which
		# can be kinda slow if we're writing expected and
		# actual results to disk.
		1
		1.0.1
		1.0.2
		1.0.3
		1.1rc2
		1.1rc3
		1.1
		1.1.1
		1.1.2
		1.2rc2
		1.2rc3
		1.2rc4
		1.2rc5
		1.2
		1.2.1
		1.2.2
		# 1.3beta1
		# 1.3beta2
		# 1.3rc1
		# 1.3rc2
		# 1.3
		# 1.3.1
		# 1.3.2
		# 1.3.3
		# 1.4beta1
		# 1.4rc1
		# 1.4rc2
		# 1.4
		# 1.4.1
		# 1.4.2
		# 1.4.3
		# 1.5beta1
		# 1.5beta2
		# 1.5beta3
		# 1.5rc1
		# 1.5
		# 1.5.1
		# 1.5.2
		# 1.5.3
		# 1.5.4
		# 1.6beta1
		# 1.6beta2
		# 1.6rc1
		# 1.6rc2
		# 1.6
		# 1.6.1
		# 1.6.2
		# 1.6.3
		# 1.6.4
		# 1.7beta1
		# 1.7beta2
		# 1.7rc1
		# 1.7rc2
		# 1.7rc3
		# 1.7rc4
		# 1.7rc5
		# 1.7rc6
		# 1.7
		# 1.7.1
		# 1.7.2
		# 1.7.3
		# 1.7.4
		# 1.7.5
		# 1.7.6

		# The 1.8 series is "interesting" because it had a
		# patch release with RCs.
		1.8beta1
		1.8beta2
		1.8rc1
		1.8rc2
		1.8rc3
		1.8
		1.8.1
		1.8.2
		1.8.3
		1.8.4
		1.8.5rc4
		1.8.5rc5
		1.8.5
		1.8.6
		1.8.7
		# 1.9beta1
		# 1.9beta2
		# 1.9rc1
		# 1.9rc2
		# 1.9
		# 1.9.1
		# 1.9.2
		# 1.9.3
		# 1.9.4
		# 1.9.5
		# 1.9.6
		# 1.9.7
		# 1.10beta1
		# 1.10beta2
		# 1.10rc1
		# 1.10rc2
		# 1.10
		# 1.10.1
		# 1.10.2
		# 1.10.3
		# 1.10.4
		# 1.10.5
		# 1.10.6
		# 1.10.7
		# 1.10.8
		1.11beta1
		1.11beta2
		1.11beta3
		1.11rc1
		1.11rc2
		1.11
		1.11.1
		1.11.2
		1.11.3
		1.11.4
		1.11.5
		1.11.6
		1.11.7
		1.11.8
		1.11.9
		1.11.10
		1.11.11
		1.11.12
		1.11.13
		1.12beta1
		1.12beta2
		1.12rc1
		1.12
		1.12.1
		1.12.2
		1.12.3
		1.12.4
		1.12.5
		1.12.6
		1.12.7
		1.12.8
		1.12.9
	)

	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk

		expected/%/all: ; @mkdir $(@D) && $(MAKE) _prelude.go.VERSION=$* $(addprefix expected/$*/,$(expected_all))
		expected/$(_prelude.go.VERSION)/%: ; @echo $(if $(filter $(expected_have),$*),true,false) > $@
		.PHONY: expected/%/all

		actual/all: $(addsuffix /all,$(patsubst expected/%,actual/%,$(wildcard expected/*)))
		actual/%/all: ; @mkdir $(@D) && $(MAKE) _prelude.go.VERSION=$* $(patsubst expected/%,actual/%,$(wildcard expected/$*/*))
		actual/$(_prelude.go.VERSION)/%: ; @echo $(if $(call _prelude.go.VERSION.HAVE,$*),true,false) > $@
		.PHONY: actual/all actual/%/all
	__EOT__

	mkdir expected
	local a b have=()
	for a in "${versions[@]}"; do
		have+=("$a")
		make -j128 _prelude.go.HAVE=/phony/go _prelude.go.VERSION=unsed expected_all="${versions[*]}" expected_have="${have[*]}" "expected/$a/all"
	done

	mkdir actual
	make -j128 _prelude.go.HAVE=/phony/go _prelude.go.VERSION=unset actual/all

	truncated_diff -ruN expected actual
}
