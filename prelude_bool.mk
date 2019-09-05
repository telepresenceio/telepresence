# This is part of `prelude.mk`, split out for organizational purposes.
# !!! NOTHING EAGER IS ALLOWED TO HAPPEN IN THIS FILE !!!

#
# Boolean support

# $(TRUE) is a non-empty string; $(FALSE) is an empty string.
TRUE  = T
FALSE =

# Usage: $(call not,BOOL)
not = $(if $1,$(FALSE),$(TRUE))
