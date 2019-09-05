#!/usr/bin/env bats

load common

@test "prelude_bool.mk: TRUE" {
	check_expr_eq strict '$(if $(TRUE),pass,fail)' 'pass'
}

@test "prelude_bool.mk: FALSE" {
	check_expr_eq strict '$(if $(FALSE),fail,pass)' 'pass'
}

@test "prelude_bool.mk: not TRUE" {
	check_expr_eq strict '$(if $(call not,$(TRUE)),fail,pass)' 'pass'
}

@test "prelude_bool.mk: not FALSE" {
	check_expr_eq strict '$(if $(call not,$(FALSE)),pass,fail)' 'pass'
}

@test "prelude_bool.mk: not truthy" {
	check_expr_eq strict '$(if $(call not,truthy),fail,pass)' 'pass'
}
