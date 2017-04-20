#!/bin/bash
set -e

# Clone blackbird-homebrew:
BUILD_HOMEBREW_DIR=$(mktemp --directory)
echo "Cloning into ${BUILD_HOMEBREW_DIR}..."
git clone git@github.com:datawire/homebrew-blackbird.git "${BUILD_HOMEBREW_DIR}"
FORMULA="${BUILD_HOMEBREW_DIR}/Formula/telepresence.rb"

# Update recipe
cp ci/homebrew-formula.rb "$FORMULA"
sed "s/__NEW_VERSION__/${TELEPRESENCE_VERSION}/g" -i "$FORMULA"
TARBALL_HASH=$(curl --silent -L "https://github.com/datawire/telepresence/archive/${TELEPRESENCE_VERSION}.tar.gz" | sha256sum | cut -f 1 -d " ")
sed "s/__TARBALL_HASH__/${TARBALL_HASH}/g" -i "$FORMULA"
cd "${BUILD_HOMEBREW_DIR}"
git add "$FORMULA"
git commit -m "Release ${TELEPRESENCE_VERSION}"
git push origin master
