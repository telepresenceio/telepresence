#!/usr/bin/env bats

load common

@test "prelude_path.mk: path.trimprefix" {
	check_expr_eq echo '$(call path.trimprefix,foo/bar,foo/bar foo/bar/baz qux)' '. baz qux'
}

@test "prelude_path.mk: path.addprefix" {
	check_expr_eq echo '$(call path.addprefix,foo/bar,. baz)' 'foo/bar foo/bar/baz'
}
