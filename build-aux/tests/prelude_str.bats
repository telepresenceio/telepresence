#!/usr/bin/env bats

load common

@test "prelude_str.mk: NL" {
	# Honestly, this checks `quote.shell` more than it does NL.
	check_expr_eq strict '$(NL)' $'\n'
}

@test "prelude_str.mk: SPACE" {
	# Honestly, this checks `quote.shell` more than it does SPACE.
	check_expr_eq strict '$(SPACE)' ' '
}

@test "prelude_str.mk: str.eq" {
	cat >>Makefile <<-'__EOT__'
		include build-aux/prelude.mk
		test = $(info ("$1" == "$2") => $(if $(call str.eq,$1,$2),$$(TRUE),$$(FALSE)))
		$(call test,foo,foo)
		$(call test,foo,bar)
		$(call test,bar,bar)
		$(call test,f%,foo)
		$(call test,foo,f%)
		$(call test,f%,f%)
		all: noop
		noop: ; @true
		.PHONY: noop
	__EOT__

	make >actual
	printf '("%s" == "%s") => $(%s)\n' >expected \
	       foo foo TRUE \
	       foo bar FALSE \
	       bar bar TRUE \
	       f% foo FALSE \
	       foo f% FALSE \
	       f% f% TRUE
	diff -u expected actual
}
