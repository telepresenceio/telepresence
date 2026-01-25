#!/usr/bin/env bash
set -e

# Validate that VERSION is set and a valid SemVer
[[ -z "${VERSION}" ]] && { echo "Error: VERSION required" >&2; exit 1; }
if ! [[ "$VERSION" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-([0-9A-Za-z\.-]+))?(\+([0-9A-Za-z\.-]+))?$ ]]; then
    echo "Error: VERSION='$VERSION' is not a valid SemVer string." >&2
    exit 1
fi

# Only run on Darwin. Other OSes are unlikely to have the pkgbuild and productbuild commands
if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "Error: This script only supports macOS (Darwin)." >&2
    exit 1
fi

# Allow ARCH override from environment (for CI cross-builds), otherwise detect
if [[ -n "${ARCH}" ]]; then
    arch="${ARCH}"
else
    raw_arch=$(uname -m)
    case "$raw_arch" in
        arm64)
            arch="arm64"
            ;;
        x86_64)
            arch="amd64"
            ;;
        *)
            echo "Error: Unsupported architecture: $raw_arch" >&2
            echo "This script only supports arm64 (Apple Silicon) and x86_64 (Intel)." >&2
            exit 1
            ;;
    esac
fi

build_output=../../build-output
mkdir -p "$build_output"
telepresence_binary="$build_output/telepresence-$VERSION"
telepresence_url="https://github.com/telepresenceio/telepresence/releases/download/v${VERSION}/telepresence-darwin-${arch}"

# For CI builds, check if a local binary exists from the main build process
local_binary="$build_output/bin/telepresence"
if [[ -f "$local_binary" ]] && file "$local_binary" | grep -q "Mach-O"; then
  echo "Using local build: $local_binary"
  cp "$local_binary" "$telepresence_binary"
elif [[ ! -f "$telepresence_binary" ]]; then
  # Download only if missing OR outdated
  curl -L --fail --remote-time --output "$telepresence_binary" "$telepresence_url" || exit 1
else
  curl -L --fail --remote-time --output "$telepresence_binary" --time-cond "$telepresence_binary" "$telepresence_url" || exit 1
fi

# === Build CLI-only package ===
cli_payload="$build_output/cli/Payload"
mkdir -p "$cli_payload/usr/local/bin"
cp "$telepresence_binary" "$cli_payload/usr/local/bin/telepresence"
chmod +x "$cli_payload/usr/local/bin/telepresence"

cp uninstall "$cli_payload/usr/local/bin/telepresence-uninstall"
chmod +x "$cli_payload/usr/local/bin/telepresence-uninstall"

product="$build_output/product"
mkdir -p "$product"
sed "s|__VERSION__|$VERSION|g" Distribution.xml > "$product/Distribution.xml"

pkgbuild --identifier io.telepresence.cli \
         --version "$VERSION" \
         --root "$cli_payload" \
         --install-location / \
         "$product/cli.pkg"

# === Build Rootd package ===
rootd_payload="$build_output/rootd/Payload"
mkdir -p "$rootd_payload/usr/local/bin"
cp telepresence-rootd "$rootd_payload/usr/local/bin"
chmod +x "$rootd_payload/usr/local/bin/telepresence-rootd"

mkdir -p "$rootd_payload/Library/LaunchDaemons"
cp io.telepresence.rootd.plist "$rootd_payload/Library/LaunchDaemons/"

mkdir -p "$rootd_payload/etc/newsyslog.d"
cp syslog.conf "$rootd_payload/etc/newsyslog.d/telepresence-rootd.conf"

rootd_scripts="$build_output/rootd/Scripts"
mkdir -p "$rootd_scripts"
cp postinstall "$rootd_scripts/postinstall"
chmod +x "$rootd_scripts/postinstall"

pkgbuild --identifier io.telepresence.rootd \
         --version "$VERSION" \
         --root "$rootd_payload" \
         --scripts "$rootd_scripts" \
         --install-location / \
         "$product/rootd.pkg"

resources="$build_output/resources"
mkdir -p "$resources"
cp welcome.rtf "$resources/welcome.rtf"
textutil -convert rtf -stdin -stdout < ../../LICENSE > "$resources/license.rtf"

productbuild --distribution "$product/Distribution.xml" \
             --package-path "$product" \
             --resources "$resources" \
             --version "$VERSION" \
             "$build_output/Telepresence.pkg"

rm -rf cli rootd resources product
