#!/usr/bin/env bats

load common

# These tests are ordered such that it makes sense to debug them from
# top to bottom.

@test "prelude_uint.mk: _uint.normalize" {
	check_expr_eq strict '$(call _uint.normalize,4)' '4'
	check_expr_eq strict '$(call _uint.normalize,0)' '0'
	check_expr_eq strict '$(call _uint.normalize,004)' '4'
	check_expr_eq strict '$(call _uint.normalize, 4)' '4'
	check_expr_eq strict '$(call _uint.normalize,4 )' '4'
	# check that it errors for things that aren't uint
	not make EXPR='$(call _uint.normalize,)' expr-eq-strict-actual
	not make EXPR='$(call _uint.normalize,  )' expr-eq-strict-actual
	not make EXPR='$(call _uint.normalize,a)' expr-eq-strict-actual
	not make EXPR='$(call _uint.normalize,9a)' expr-eq-strict-actual
	not make EXPR='$(call _uint.normalize,a9)' expr-eq-strict-actual
	not make EXPR='$(call _uint.normalize,-4)' expr-eq-strict-actual
	not make EXPR='$(call _uint.normalize,4.2)' expr-eq-strict-actual
}

@test "prelude_uint.mk: _uintx.to.uint" {
	check_expr_eq strict '$(call _uintx.to.uint,x x x x)' '4'
}

@test "prelude_uint.mk: _uintx.from.uint.helper" {
	check_expr_eq echo '$(call _uintx.from.uint.helper,3,)' 'x x x'
}

@test "prelude_uint.mk: _uintx.from.uint" {
	check_expr_eq strict '$(call _uintx.from.uint,4)' 'x x x x'
}

@test "prelude_uint.mk: _uintx.max" {
	check_expr_eq strict '$(call _uintx.max,x x x,x x)' 'x x x'
}

@test "prelude_uint.mk: _uintx.min" {
	check_expr_eq strict '$(call _uintx.min,x x x,x x)' 'x x'
}

@test "prelude_uint.mk: uint.max" {
	check_expr_eq strict '$(call uint.max,3,2)' '3'
}

@test "prelude_uint.mk: uint.min" {
	check_expr_eq strict '$(call uint.min,3,2)' '2'
}

@test "prelude_uint.mk: uint.eq" {
	check_expr_eq strict '$(if $(call uint.eq,3,3),true,false)' 'true'
	check_expr_eq strict '$(if $(call uint.eq,3,4),true,false)' 'false'
	check_expr_eq strict '$(if $(call uint.eq,3,03),true,false)' 'true'
	check_expr_eq strict '$(if $(call uint.eq,  3  , 03),true,false)' 'true'
}

@test "prelude_uint.mk: uint.ge" {
	check_expr_eq strict '$(if $(call uint.ge,2,3),true,false)' 'false'
	check_expr_eq strict '$(if $(call uint.ge,3,3),true,false)' 'true'
	check_expr_eq strict '$(if $(call uint.ge,4,3),true,false)' 'true'
}

@test "prelude_uint.mk: uint.le" {
	check_expr_eq strict '$(if $(call uint.le,2,3),true,false)' 'true'
	check_expr_eq strict '$(if $(call uint.le,3,3),true,false)' 'true'
	check_expr_eq strict '$(if $(call uint.le,4,3),true,false)' 'false'
}

@test "prelude_uint.mk: uint.gt" {
	check_expr_eq strict '$(if $(call uint.gt,2,3),true,false)' 'false'
	check_expr_eq strict '$(if $(call uint.gt,3,3),true,false)' 'false'
	check_expr_eq strict '$(if $(call uint.gt,4,3),true,false)' 'true'
}

@test "prelude_uint.mk: uint.lt" {
	check_expr_eq strict '$(if $(call uint.lt,2,3),true,false)' 'true'
	check_expr_eq strict '$(if $(call uint.lt,3,3),true,false)' 'false'
	check_expr_eq strict '$(if $(call uint.lt,4,3),true,false)' 'false'
}
