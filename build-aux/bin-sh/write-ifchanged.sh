#!/usr/bin/env bash
# Copyright (C) 2015  Luke Shumaker
# Copyright 2019 Datawire
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU Affero General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.
#
# This program is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU Affero General Public License for more details.
#
# You should have received a copy of the GNU Affero General Public License
# along with this program.  If not, see <http://www.gnu.org/licenses/>.

outfile=$1
tmpfile="$(dirname "$outfile")/.tmp.${outfile##*/}.tmp"

cat > "$tmpfile" || exit $?
if cmp -s "$tmpfile" "$outfile"; then
        rm -f "$tmpfile" || :
else
	if [[ -n "$CI" && -e "$outfile" ]]; then
		echo "error: This should not happen in CI: ${outfile} should not change" >&2
		exit 1
	fi
        mv -f "$tmpfile" "$outfile"
fi
