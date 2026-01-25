#!/bin/bash
set -e

# Build .deb and .rpm packages using nfpm

# Validate required environment variables
[[ -z "${VERSION}" ]] && { echo "Error: VERSION required" >&2; exit 1; }
[[ -z "${ARCH}" ]] && { echo "Error: ARCH required (amd64 or arm64)" >&2; exit 1; }

# Only run on Linux
if [[ "$(uname -s)" != "Linux" ]]; then
    echo "Error: This script only supports Linux." >&2
    exit 1
fi

# Parse version for package managers (RPM doesn't allow hyphens in version)
# Convert 2.27.0-test.4 to version=2.27.0 release=test.4
if [[ "$VERSION" =~ ^([0-9]+\.[0-9]+\.[0-9]+)-(.+)$ ]]; then
    PKG_VERSION="${BASH_REMATCH[1]}"
    PKG_RELEASE="${BASH_REMATCH[2]}"
else
    PKG_VERSION="$VERSION"
    PKG_RELEASE="1"
fi
export PKG_VERSION PKG_RELEASE

echo "Building packages: version=${PKG_VERSION}, release=${PKG_RELEASE}, arch=${ARCH}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
build_output="$(cd "${script_dir}/../.." && pwd)/build-output"
mkdir -p "$build_output/release"

echo "script_dir=$script_dir"
echo "build_output=$build_output"

# For CI builds, check if a local binary exists from the main build process
local_binary="${build_output}/bin/telepresence"
echo "Checking for local binary at: $local_binary"
if [[ -f "$local_binary" ]] && file "$local_binary" | grep -q "ELF"; then
    echo "Using local build: $local_binary"
    BINDIR="${build_output}/bin"
else
    # Download from GitHub releases
    echo "Downloading telepresence v${VERSION} for linux-${ARCH}..."
    mkdir -p "${build_output}/bin"
    curl -L --fail --remote-time \
        -o "${build_output}/bin/telepresence" \
        "https://github.com/telepresenceio/telepresence/releases/download/v${VERSION}/telepresence-linux-${ARCH}"
    chmod +x "${build_output}/bin/telepresence"
    BINDIR="${build_output}/bin"
fi

export BINDIR
echo "BINDIR=$BINDIR"
echo "Binary exists: $(ls -la "$BINDIR/telepresence" 2>&1)"

cd "$script_dir"

# Map arch to nfpm format
case "$ARCH" in
    amd64)
        nfpm_arch="amd64"
        ;;
    arm64)
        nfpm_arch="arm64"
        ;;
    *)
        echo "Error: Unsupported architecture: $ARCH" >&2
        exit 1
        ;;
esac

echo "Building packages for linux-${ARCH}, version ${VERSION}..."

# Export all variables needed by nfpm.yaml.in and preprocess with envsubst
export ARCH="$nfpm_arch"
# PKG_VERSION, PKG_RELEASE, and BINDIR are already exported above

echo "Environment for nfpm: ARCH=$ARCH PKG_VERSION=$PKG_VERSION PKG_RELEASE=$PKG_RELEASE BINDIR=$BINDIR"
envsubst < nfpm.yaml.in > nfpm.yaml

# Build .deb package
echo "Building .deb package..."
nfpm package \
    --config nfpm.yaml \
    --packager deb \
    --target "${build_output}/release/telepresence-${VERSION}-linux-${ARCH}.deb"

# Build .rpm package
echo "Building .rpm package..."
nfpm package \
    --config nfpm.yaml \
    --packager rpm \
    --target "${build_output}/release/telepresence-${VERSION}-linux-${ARCH}.rpm"

echo ""
echo "Packages built successfully:"
ls -la "${build_output}/release/"*.deb "${build_output}/release/"*.rpm 2>/dev/null || true