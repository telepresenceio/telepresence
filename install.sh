#!/bin/bash

# Install Telepresence binaries in ${PREFIX}/bin.

set -o errexit
set -o pipefail
set -o nounset
# set -o xtrace

echo "Installing Telepresence in ${PREFIX}"

SRCDIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
BINDIR="${BINDIR:-${PREFIX}/bin}"

# Setup
BLDDIR=$(mktemp -d)
trap "rm -rf $BLDDIR" EXIT
DIST="${BLDDIR}/dist"
mkdir -p "${DIST}"

# Build executables in dist
cd "${SRCDIR}"
python3 packaging/build-telepresence.py "${DIST}/telepresence"
python3 packaging/build-sshuttle.py "${DIST}/sshuttle-telepresence"

# Place binaries
install -d "${BINDIR}"
install \
    "${DIST}/sshuttle-telepresence" \
    "${DIST}/telepresence" \
    "${BINDIR}"

# Make sure things appear to run
VERSION=$("${BINDIR}/telepresence" --version)
echo "Installed Telepresence ${VERSION}"
