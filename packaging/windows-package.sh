#!/bin/bash
set -e -x

# This is a scrappy first attempt at a windows "installer".
# It generates a zip file with all the dependencies and things required
# for running telepresence in windows. We should eventually change this
# to produce a msi, but for developer preview this is likely fine.

if [ -z "$TELEPRESENCE_VERSION" ]
then
   echo "Must set version"
   exit 1
fi

if [ -z "$GOARCH" ]
then
   echo "Must set GOARCH"
   exit 1
fi

SCRIPT_DIR=$( dirname -- "${BASH_SOURCE[0]}")

WINFSP_VERSION=1.11.22176
SSHFS_WIN_VERSION=3.7.21011
WINTUN_VERSION=0.14.1
# SHA2-256 Checksum for wintun-0.14.1.zip
WINTUN_CHECKSUM=07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51
BINDIR="${BINDIR:-./build-output/bin}"

rm -f "${BINDIR}/telepresence.zip"
rm -f "${BINDIR}/telepresence-setup.exe"

ZIPDIR="${ZIPDIR:-$BINDIR/telepresence-windows}"
rm -rf "$ZIPDIR"
mkdir -p "$ZIPDIR"

if [[ ! "${ZIPDIR}" ]]; then
    echo "Could not create $ZIPDIR for windows package"
    exit 1
fi

# Download sshfs-win.msi + winfsp.msi
# ${WINFSP_VERSION%.*} will remove the last `.` and everything after it
curl -L -o "${ZIPDIR}/winfsp.msi" "https://github.com/billziss-gh/winfsp/releases/download/v${WINFSP_VERSION%.*}/winfsp-${WINFSP_VERSION}.msi"
curl -L -o "${ZIPDIR}/sshfs-win.msi" "https://github.com/billziss-gh/sshfs-win/releases/download/v${SSHFS_WIN_VERSION}/sshfs-win-${SSHFS_WIN_VERSION}-x64.msi"

# Download wintun
curl -L -o "${BINDIR}/wintun.zip" "https://www.wintun.net/builds/wintun-${WINTUN_VERSION}.zip"
echo "${WINTUN_CHECKSUM}  ${BINDIR}/wintun.zip" | sha256sum -c -
unzip -p -C "${BINDIR}/wintun.zip" "wintun/bin/${GOARCH}/wintun.dll" > "${ZIPDIR}/wintun.dll"

cp "${BINDIR}/telepresence.exe" "${ZIPDIR}/telepresence.exe"

# Copy powershell install script into $ZIPDIR
cp "${SCRIPT_DIR}/install-telepresence.ps1" "${ZIPDIR}/install-telepresence.ps1"

powershell -Command "Compress-Archive -Path '${ZIPDIR}/*' -DestinationPath '${BINDIR}/telepresence.zip'"

# Generate installer
cp "${SCRIPT_DIR}/sidebar.png" "${ZIPDIR}/sidebar.png"
TELEPRESENCE_PLAIN_VERSION=${TELEPRESENCE_VERSION#v}
TELEPRESENCE_PLAIN_VERSION=${TELEPRESENCE_PLAIN_VERSION%-*}
sed s/TELEPRESENCE_VERSION/"$TELEPRESENCE_PLAIN_VERSION"/ "${SCRIPT_DIR}/telepresence.wxs.in" > "${ZIPDIR}/telepresence.wxs"
sed s/TELEPRESENCE_VERSION/"$TELEPRESENCE_PLAIN_VERSION"/ "${SCRIPT_DIR}/bundle.wxs.in" > "${ZIPDIR}/bundle.wxs"

WIX_VERSION=4.0.4
dotnet tool install --global wix --version $WIX_VERSION

cd "${ZIPDIR}"
wix build -o telepresence.msi telepresence.wxs
wix extension add -g WixToolset.Bal.wixext/$WIX_VERSION
wix build -ext WixToolset.Bal.wixext/$WIX_VERSION -o "${BINDIR}/telepresence-setup.exe" bundle.wxs

rm -rf "${ZIPDIR}"
