#!/bin/bash
set -e

if [ -z "$1" ]
then
   echo "Must set version"
   exit 1
fi

VERSION=$1

BINDIR="${BINDIR:-./build-output/bin}"

# Clone blackbird-homebrew:
BUILD_HOMEBREW_DIR=$(mktemp -d)
echo "Cloning into ${BUILD_HOMEBREW_DIR}..."
git clone git@github.com:datawire/homebrew-blackbird.git "${BUILD_HOMEBREW_DIR}"
FORMULA="${BUILD_HOMEBREW_DIR}/Formula/telepresence.rb"

# Update recipe
cp packaging/homebrew-formula.rb "$FORMULA"
sed -i'' -e "s/__NEW_VERSION__/${VERSION}/g" "$FORMULA"
TARBALL_HASH=$(shasum -a 256 $BINDIR/telepresence | cut -f 1 -d " ")

# We don't want to update our homebrew formula if there
# isn't a hash, so exit early if that's the case.
if [ -z "${TARBALL_HASH}" ]
then 
    echo "Telepresence binary could not be hashed"
    exit 1
fi

sed -i'' -e "s/__TARBALL_HASH__/${TARBALL_HASH}/g" "$FORMULA"
chmod 644 "$FORMULA"
cd "${BUILD_HOMEBREW_DIR}"

# Use the correct machine user for committing
git config user.email "services@datawire.io"
git config --global user.name "d6e automaton"

git add "$FORMULA"
git commit -m "Release ${VERSION}"

# This cat is just so we can see the formula in case
# the git permissions are incorrect and we can't publish
# the change. Once we know the automation is working, we can
# remove it.
cat  ${FORMULA}
git push origin master
