#!/bin/bash
set -eo pipefail
IFS=$'\n\t'
# http://redsymbol.net/articles/unofficial-bash-strict-mode/
# because I don't know what I'm doing in Bash

set -x

# Build the documentation as usual
cd "$(dirname "$0")"
npm install
npm run build

# Remove the data-path attributed of every list item linking to index.html,
# which are the ones marked with data-level="1.1". This causes the GitBook
# scripts to redirect to the index page rather fetching and replacing just
# the content area, as they do for proper GitBook-generated pages.

perl -pi \
    -e "s/{VERSION}/$VERSION/g;" \
    -e 's,<li class="chapter " data-level="1.1" data-path="[^"]*">,<li class="chapter " data-level="1.1">,g;' \
    $(find _book -name '*.html') _book/search_index.json

# Replace index.html with our hand-crafted landing page
cp index.html _book/

# Copy YAML into _book/ as well.
# cp -prv yaml _book/
