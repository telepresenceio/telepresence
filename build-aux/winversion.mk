# TELEPRESENCE_WINVERSION is the four-field version Windows needs, derived
# from TELEPRESENCE_VERSION: X.Y.Z.100 for a GA release and X.Y.Z.N for
# pre-release number N (v2.32.0 -> 2.32.0.100, v2.32.0-rc.4 -> 2.32.0.4).
_winver_parts = $(subst -, ,$(TELEPRESENCE_VERSION:v%=%))
_winver_pre = $(word 2,$(_winver_parts))
_winver_n = $(if $(_winver_pre),$(or $(word 2,$(subst ., ,$(_winver_pre))),0),100)
TELEPRESENCE_WINVERSION = $(word 1,$(_winver_parts)).$(_winver_n)
