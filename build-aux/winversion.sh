#!/bin/bash
# Prints the four-field Windows version for a Telepresence version string:
# X.Y.Z.100 for a GA release, X.Y.Z.N for a pre-release with number N
# (v2.32.0 -> 2.32.0.100, v2.32.0-rc.4 -> 2.32.0.4).
set -e
v=${1#v}
base=${v%%-*}
if [ "$base" = "$v" ]; then
  n=100
else
  pre=${v#*-}
  n=${pre#*.}
  n=${n%%[^0-9]*}
  [ -n "$n" ] || n=0
fi
echo "$base.$n"
