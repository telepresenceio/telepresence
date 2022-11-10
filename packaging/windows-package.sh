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

WINFSP_VERSION=1.11.22176
SSHFS_WIN_VERSION=3.7.21011
WINTUN_VERSION=0.14.1
BINDIR="${BINDIR:-./build-output/bin}"

ZIPDIR="${ZIPDIR:-./telepresence-windows}"
mkdir -p "$ZIPDIR"

if [[ ! "${ZIPDIR}" ]]; then
    echo "Could not create $ZIPDIR for windows package"
    exit 1
fi

# Download sshfs-win.msi + winfsp.msi
# ${WINFSP_VERSION%.*} will remove the last `.` and everything after it
curl -L -o "${ZIPDIR}"/winfsp.msi "https://github.com/billziss-gh/winfsp/releases/download/v${WINFSP_VERSION%.*}/winfsp-${WINFSP_VERSION}.msi"
curl -L -o "${ZIPDIR}"/sshfs-win.msi "https://github.com/billziss-gh/sshfs-win/releases/download/v${SSHFS_WIN_VERSION}/sshfs-win-${SSHFS_WIN_VERSION}-x64.msi"

# Download wintun
curl -L -o "${BINDIR}"/wintun.zip "https://www.wintun.net/builds/wintun-${WINTUN_VERSION}.zip"
unzip -C "${BINDIR}"/wintun.zip wintun/bin/amd64/wintun.dll -p > "${ZIPDIR}/wintun.dll"

cp "${BINDIR}/telepresence.exe" "${ZIPDIR}/telepresence.exe"

# Copy powershell install script into $ZIPDIR
cp $( dirname -- "${BASH_SOURCE[0]}")/install-telepresence.ps1 "${ZIPDIR}/install-telepresence.ps1"

zip -r -j "${BINDIR}/telepresence.zip" "${ZIPDIR}"

rm -rf "${ZIPDIR}"
