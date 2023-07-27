#!/bin/bash
set -e

if [ -z "$1" ]
then
   echo "Must set version"
   exit 1
fi

VERSION="${1}"

ARCH=(amd64 arm64)
OS=(darwin linux)

WORK_DIR="$(mktemp -d)"
echo "Working in ${WORK_DIR}"

BUILD_HOMEBREW_DIR=${WORK_DIR}/homebrew
FORMULA="${BUILD_HOMEBREW_DIR}/Formula/telepresence.rb"
FORMULA_NAME="Telepresence"

for this_os in "${OS[@]}"; do
    for this_arch in "${ARCH[@]}"; do

        if [ "${this_arch}" == "arm64" ] && [ "${this_os}" == "linux" ]; then
            # TODO support linux arm64
            continue
        fi

        # We should only be updating homebrew with a version of telepresence that
        # already exists, so let's download it
        echo "Downloading ${this_os}/${this_arch}/${VERSION}/telepresence"
        mkdir -p "${WORK_DIR}/${this_os}/${this_arch}/"
        curl -fL "https://app.getambassador.io/download/tel2/${this_os}/${this_arch}/${VERSION}/telepresence" -o "${WORK_DIR}/${this_os}/${this_arch}/telepresence"
        declare -x "TARBALL_HASH_${this_os}_${this_arch}"=$(shasum -a 256 "${WORK_DIR}/${this_os}/${this_arch}/telepresence" | cut -f 1 -d " ")
        tmp_var=TARBALL_HASH_${this_os}_${this_arch}
        echo "${tmp_var} == ${!tmp_var}"
    done
done

export HASH_ERRORS=0

for this_os in "${OS[@]}"; do
    for this_arch in "${ARCH[@]}"; do

        if [ "${this_arch}" == "arm64" ] && [ "${this_os}" == "linux" ]; then
            # TODO support linux arm64
            continue
        fi

        # We don't want to update our homebrew formula if there
        # isn't a hash, so exit early if that's the case.
        tmp_var="TARBALL_HASH_${this_os}_${this_arch}"
        if [ -n "${!tmp_var}" ]; then
            echo "Telepresence binary hash: ${tmp_var} == ${!tmp_var}"
        else
            echo "Telepresence binary could not be hashed: ${tmp_var}"
            HASH_ERRORS=$((HASH_ERRORS++))
        fi
    done
done

echo "HASH_ERRORS==${HASH_ERRORS}"

if [ "${HASH_ERRORS}" -gt 0 ]; then
    exit 1
fi

# Clone blackbird-homebrew:
echo "Cloning into ${BUILD_HOMEBREW_DIR}..."
git clone https://github.com/datawire/homebrew-blackbird.git "${BUILD_HOMEBREW_DIR}"

# Update recipe
cp packaging/homebrew-formula.rb "$FORMULA"

sed -i'' -e "s/__FORMULA_NAME__/${FORMULA_NAME}/g" "$FORMULA"
sed -i'' -e "s/__NEW_VERSION__/${VERSION}/g" "$FORMULA"

for this_os in "${OS[@]}"; do
    for this_arch in "${ARCH[@]}"; do

        if [ "${this_arch}" == "arm64" ] && [ "${this_os}" == "linux" ]; then
            # TODO support linux arm64
            continue
        fi
        tmp_var="TARBALL_HASH_${this_os}_${this_arch}"
        sed -i'' -e "s/__TARBALL_HASH_${this_os^^}_${this_arch^^}__/${!tmp_var}/g" "$FORMULA"
    done
done

chmod 644 "$FORMULA"
cd "${BUILD_HOMEBREW_DIR}"

# Use the correct machine user for committing
git config user.email "services@datawire.io"
git config user.name "d6e automaton"

git add "$FORMULA"
git commit -m "Release ${VERSION}"

# This cat is just so we can see the formula in case
# the git permissions are incorrect and we can't publish
# the change. Once we know the automation is working, we can
# remove it.
cat "${FORMULA}"
git push origin master

# Clean up the working directory
rm -rf "${WORK_DIR}"
