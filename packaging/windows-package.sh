#!/bin/bash
set -e

# This is a scrappy first attempt at a windows "installer".
# It generates a zip file with all the dependencies and things required
# for running telepresence in windows. We should eventually change this
# to produce a msi, but for developer preview this is likely fine.

if [ -z "$TELEPRESENCE_VERSION" ]
then
   echo "Must set version"
   exit 1
fi

BINDIR="${BINDIR:-./build-output/bin}"

ZIPDIR="${ZIPDIR:-./telepresence-windows}"
mkdir -p "$ZIPDIR"

if [[ ! "${ZIPDIR}" ]]; then
    echo "Could not create $ZIPDIR for windows package"
    exit 1
fi

# Download sshfs-win.msi + winfsp.msi
curl -L -o "${ZIPDIR}"/winfsp.msi https://github.com/billziss-gh/winfsp/releases/download/v1.9/winfsp-1.9.21096.msi
curl -L -o "${ZIPDIR}"/sshfs-win.msi https://github.com/billziss-gh/sshfs-win/releases/download/v3.7.21011/sshfs-win-3.7.21011-x64.msi

# Download wintun
curl -L https://www.wintun.net/builds/wintun-0.12.zip | tar -vf - -Ox wintun/bin/amd64/wintun.dll > "${ZIPDIR}/wintun.dll"
cp "${BINDIR}/telepresence.exe" "${ZIPDIR}/telepresence.exe"

# Copy powershell install script into $ZIPDIR
cp packaging/install-telepresence.ps1 "${ZIPDIR}/install-telepresence.ps1"

zip -r -j telepresence.zip "${ZIPDIR}"

rm -rf "${ZIPDIR}"
