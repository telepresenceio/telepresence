#!/bin/bash
set -e

# Clone blackbird-homebrew:
BUILD_HOMEBREW_DIR=$(mktemp -d)
echo "Cloning into ${BUILD_HOMEBREW_DIR}..."
git clone git@github.com:datawire/homebrew-blackbird.git "${BUILD_HOMEBREW_DIR}"
FORMULA="${BUILD_HOMEBREW_DIR}/Formula/telepresence.rb"

# Update recipe
cp dist/homebrew-formula.rb "$FORMULA"
sed "s/__NEW_VERSION__/${TELEPRESENCE_VERSION}/g" -i "$FORMULA"
TARBALL_HASH=$(sha256sum dist/telepresence-${TELEPRESENCE_VERSION}.tar.gz | cut -f 1 -d " ")
sed "s/__TARBALL_HASH__/${TARBALL_HASH}/g" -i "$FORMULA"
chmod 644 "$FORMULA"
cd "${BUILD_HOMEBREW_DIR}"
git add "$FORMULA"
git commit -m "Release ${TELEPRESENCE_VERSION}"
git push origin master
