#!/usr/bin/env bash
# Copyright 2019 Datawire. All rights reserved.

if cmp -s "$1" "$2"; then
	rm -f "$1" || :
else
	if [[ -n "$CI" && -e "$2" ]]; then
		echo "error: This should not happen in CI: $2 should not change" >&2
		exit 1
	fi
	mv -f "$1" "$2"
fi
