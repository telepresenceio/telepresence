#!/bin/bash

# Install Telepresence in ${PREFIX}/share/telepresence,
# binaries in ${PREFIX}/bin.

set -o errexit
set -o pipefail
set -o nounset
# set -o xtrace

echo "Installing Telepresence in ${PREFIX}"

SRCDIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
DATADIR="${DATADIR:-${PREFIX}/share/telepresence}"
VENVDIR="${VENVDIR:-${DATADIR}/libexec}"
BINDIR="${BINDIR:-${PREFIX}/bin}"

# Create a virtualenv and install into it
python3 -m venv "${VENVDIR}"
"${VENVDIR}/bin/pip" -q install "git+https://github.com/datawire/sshuttle.git@telepresence"
"${VENVDIR}/bin/pip" -q install "${SRCDIR}"

# Remove unnecessary packages and wheels
"${VENVDIR}/bin/pip" -q uninstall -y pip
rm -rf "${VENVDIR}/share"

# Place binaries
install -d "${BINDIR}"
install \
 "${VENVDIR}/bin/sshuttle-telepresence" \
 "${VENVDIR}/bin/stamp-telepresence" \
 "${VENVDIR}/bin/telepresence" \
 "${BINDIR}"

# Make sure things appear to run
VERSION=$("${BINDIR}/telepresence" --version)
echo "Installed Telepresence ${VERSION}"
