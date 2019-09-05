# This is part of `prelude.mk`, split out for organizational purposes.
# !!! NOTHING EAGER IS ALLOWED TO HAPPEN IN THIS FILE !!!

#
# String support

# NOTE: this is not a typo, this is actually how you spell newline in Make
define NL


endef
export NL

# NOTE: this is not a typo, this is actually how you spell space in Make
define SPACE
 
endef

# Usage: $(call str.eq,STR1,STR2)
# Evaluates to either $(TRUE) or $(FALSE)
str.eq = $(if $(subst x$1,,x$2)$(subst x$2,,x$1),$(FALSE),$(TRUE))
