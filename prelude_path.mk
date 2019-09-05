# This is part of `prelude.mk`, split out for organizational purposes.
# !!! NOTHING EAGER IS ALLOWED TO HAPPEN IN THIS FILE !!!

#
# Path support

# Usage: $(call path.trimprefix,PREFIX,LIST)
# Example: $(call path.trimprefix,foo/bar,foo/bar foo/bar/baz) => . baz
path.trimprefix = $(patsubst $1/%,%,$(patsubst $1,$1/.,$2))

# Usage: $(call path.addprefix,PREFIX,LIST)
# Example: $(call path.addprefix,foo/bar,. baz) => foo/bar foo/bar/baz
path.addprefix = $(patsubst %/.,%,$(addprefix $1/,$2))
