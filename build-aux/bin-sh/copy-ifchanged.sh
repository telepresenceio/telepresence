#!/usr/bin/env bash
# Copyright 2019 Datawire. All rights reserved.

if ! cmp -s "$1" "$2"; then
	if [[ -n "$CI" && -e "$2" ]]; then
		echo "error: This should not happen in CI: $2 should not change" >&2
		exit 1
	fi
	cp -f "$1" "$2"
fi
