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

# === Signing and Notarization Configuration ===
# These environment variables enable code signing and notarization:
#   MACOS_SIGN_APPLICATION - Developer ID Application certificate name (for codesign)
#   MACOS_SIGN_INSTALLER   - Developer ID Installer certificate name (for productsign)
#   MACOS_NOTARIZE_APPLE_ID - Apple ID for notarization
#   MACOS_NOTARIZE_TEAM_ID  - Apple Developer Team ID
#   MACOS_NOTARIZE_PASSWORD - App-specific password for notarization
#
# If signing variables are not set, packages will be built unsigned (suitable for local development).

sign_binary() {
    local binary="$1"
    if [[ -n "${MACOS_SIGN_APPLICATION}" ]]; then
        echo "Signing binary: $binary"
        codesign --force --options runtime --timestamp --sign "${MACOS_SIGN_APPLICATION}" "$binary"
        codesign --verify --verbose "$binary"
    fi
}

sign_package() {
    local unsigned_pkg="$1"
    local signed_pkg="$2"
    if [[ -n "${MACOS_SIGN_INSTALLER}" ]]; then
        echo "Signing package: $unsigned_pkg -> $signed_pkg"
        productsign --sign "${MACOS_SIGN_INSTALLER}" "$unsigned_pkg" "$signed_pkg"
        pkgutil --check-signature "$signed_pkg"
    else
        # No signing, just rename
        mv "$unsigned_pkg" "$signed_pkg"
    fi
}

notarize_package() {
    local pkg="$1"
    if [[ -n "${MACOS_NOTARIZE_APPLE_ID}" && -n "${MACOS_NOTARIZE_TEAM_ID}" && -n "${MACOS_NOTARIZE_PASSWORD}" ]]; then
        echo "Submitting package for notarization: $pkg"
        # Use --timeout to prevent indefinite waiting (15 minutes should be plenty)
        xcrun notarytool submit "$pkg" \
            --apple-id "${MACOS_NOTARIZE_APPLE_ID}" \
            --team-id "${MACOS_NOTARIZE_TEAM_ID}" \
            --password "${MACOS_NOTARIZE_PASSWORD}" \
            --wait \
            --timeout 15m

        echo "Stapling notarization ticket to: $pkg"
        xcrun stapler staple "$pkg"
        xcrun stapler validate "$pkg"
    else
        echo "Skipping notarization (credentials not provided)"
    fi
}

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
sign_binary "$cli_payload/usr/local/bin/telepresence"

cp uninstall "$cli_payload/usr/local/bin/telepresence-uninstall"
chmod +x "$cli_payload/usr/local/bin/telepresence-uninstall"

cli_scripts="$build_output/cli/Scripts"
mkdir -p "$cli_scripts"
cp cli-postinstall "$cli_scripts/postinstall"
chmod +x "$cli_scripts/postinstall"

product="$build_output/product"
mkdir -p "$product"
sed "s|__VERSION__|$VERSION|g" Distribution.xml > "$product/Distribution.xml"

pkgbuild --identifier io.telepresence.cli \
         --version "$VERSION" \
         --root "$cli_payload" \
         --scripts "$cli_scripts" \
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

# === Build Usage opt-out package ===
# Empty-payload package whose postinstall writes the machine-wide opt-out
# config. The installer UI conditionally selects this package only when the
# user unchecks "Help improve Telepresence by sharing anonymous usage data".
usg_payload="$build_output/usg-optout/Payload"
mkdir -p "$usg_payload"

usg_scripts="$build_output/usg-optout/Scripts"
mkdir -p "$usg_scripts"
cp usg-optout-postinstall "$usg_scripts/postinstall"
chmod +x "$usg_scripts/postinstall"

pkgbuild --identifier io.telepresence.usg-optout \
         --version "$VERSION" \
         --nopayload \
         --scripts "$usg_scripts" \
         "$product/usg-optout.pkg"

resources="$build_output/resources"
mkdir -p "$resources"
cp welcome.rtf "$resources/welcome.rtf"
textutil -convert rtf -stdin -stdout < ../../LICENSE > "$resources/license.rtf"

productbuild --distribution "$product/Distribution.xml" \
             --package-path "$product" \
             --resources "$resources" \
             --version "$VERSION" \
             "$build_output/Telepresence-unsigned.pkg"

# Sign the package (or rename if no signing certificate)
sign_package "$build_output/Telepresence-unsigned.pkg" "$build_output/Telepresence.pkg"

# Notarize the signed package (if credentials are provided)
notarize_package "$build_output/Telepresence.pkg"

rm -rf cli rootd usg-optout resources product
