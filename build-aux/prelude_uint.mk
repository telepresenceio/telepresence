# This is part of `prelude.mk`, split out for organizational purposes.
# !!! NOTHING EAGER IS ALLOWED TO HAPPEN IN THIS FILE !!!

#
# Unsigned integer support

_uint.is-uint-or-empty = $(if $1,$(call _uint.is-uint,$1),$(TRUE))
_uint.is-uint = $(and $(call str.eq,$(words $1),1),$(filter 0% 1% 2% 3% 4% 5% 6% 7% 8% 9%,$1),$(call _uint.is-uint-or-empty,$(strip \
     $(patsubst 0%,%,\
     $(patsubst 1%,%,\
     $(patsubst 2%,%,\
     $(patsubst 3%,%,\
     $(patsubst 4%,%,\
     $(patsubst 5%,%,\
     $(patsubst 6%,%,\
     $(patsubst 7%,%,\
     $(patsubst 8%,%,\
     $(patsubst 9%,%,\
                $1)))))))))))))

# Usage: $(call _uint.normalize,UINT)
# Example: $(call _uint.normalize,0) => 0
# Example: $(call _uint.normalize,004) => 4
#
# Normalizes a decimal-uint-string.  Right now, that just means
# that it $(strip)s it, and trims leading 0s.
_uint.normalize = $(strip \
    $(if $(call _uint.is-uint,$1),,$(error argument to uint.* function is not a uint: '$1'))\
    $(if $(filter 0%,$(filter-out 0,$1)),\
         $(call _uint.normalize,$(patsubst 0%,%,$1)),\
          $1))

# Usage: $(call uint.max,UINT,UINT)
# Example: $(call uint.max,3,2) => 3
uint.max = $(call _uintx.to.uint,$(call _uintx.max,$(call _uintx.from.uint,$1),$(call _uintx.from.uint,$2)))

# Usage: $(call uint.min,UINT,UINT)
# Example: $(call uint.min,3,2) => 2
uint.min = $(call _uintx.to.uint,$(call _uintx.min,$(call _uintx.from.uint,$1),$(call _uintx.from.uint,$2)))

# Usage: $(call _uint.eq,UINT,UINT)
uint.eq = $(call str.eq,$(call _uint.normalize,$1),$(call _uint.normalize,$2))

# These opperate entirely in terms of functions that call
# _uint.normalize, so they don't need to.
uint.ge = $(call uint.eq,$(call uint.max,$1,$2),$1)
uint.le = $(call uint.eq,$(call uint.min,$1,$2),$1)
uint.gt = $(and $(call uint.ge,$1,$2),$(call not,$(call uint.eq,$1,$2)))
uint.lt = $(and $(call uint.le,$1,$2),$(call not,$(call uint.eq,$1,$2)))

#
# "uintx" support: Unsigned integers represented as a list of "x"s
#
# Several operations are easier with this representation than with a
# decimal-string representation.

# Usage: $(call _uintx.to.uint,UINTX)
# Example: $(call _uintx.to.uint,x x x x) => 4
_uintx.to.uint = $(words $1)

# Usage: $(call _uintx.from.uint.helper,UINT,PARTIALL_UINTX)
# Example: $(call _uintx.from.uint.helper,3,) =>
#          $(call _uintx.from.uint.helper,3,x) =>
#          $(call _uintx.from.uint.helper,3,x x) =>
#          $(call _uintx.from.uint.helper,3,x x x) =>
#          x x x
_uintx.from.uint.helper = $(if $(call str.eq,$1,$(call _uintx.to.uint,$2)), \
                    $2, \
                    $(call _uintx.from.uint.helper,$1,$2 x))

# Usage: $(call _uintx.from.uint,UINT)
# Example: $(call _uintx.from.uint,4) => x x x x
_uintx.from.uint = $(foreach x,$(call _uintx.from.uint.helper,$(call _uint.normalize,$1),),x)

# Usage: $(call _uintx.max,UINTX,UINTX)
# Example: $(call _uintx.max,x x x,x x) => x x x
_uintx.max = $(subst xx,x,$(join $1,$2))

# Usage: $(call _uintx.min,UINTX,UINTX)
# Example: $(call _uintx.min,x x x,x x) => x x
_uintx.min = $(subst xx,x,$(filter xx,$(join $1,$2)))
