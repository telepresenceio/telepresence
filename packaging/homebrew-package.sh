#!/bin/bash
set -e

if [ -z "$1" ]
then
   echo "Must set version"
   exit 1
fi

VERSION="${1}"
PACKAGE_NAME="${2:?Can be 'tel2' or 'tel2oss'}"
GITHUB_USER="${3:-$(git config get user.name)}"
GITHUB_EMAIL="${4:-$(git config get user.email)}"
GITHUB_TOKEN="${5}"

ARCH=(amd64 arm64)
OS=(darwin linux)

MY_PATH=$(dirname "$0")
MY_PATH=$( cd "$MY_PATH" && pwd )

WORK_DIR="$(mktemp -d)"
cd "${WORK_DIR}"
echo "Working in ${WORK_DIR}"

BUILD_HOMEBREW_DIR=homebrew
if [ "${PACKAGE_NAME}" == 'tel2' ]; then
    FORMULA_NAME="Telepresence"
    FORMULA_FILE="${MY_PATH}/homebrew-formula.rb"
    FORMULA="Formula/telepresence.rb"
elif [ "${PACKAGE_NAME}" == 'tel2oss' ]; then
    FORMULA_NAME="TelepresenceOss"
    FORMULA_FILE="${MY_PATH}/homebrew-oss-formula.rb"
    FORMULA="Formula/telepresence-oss.rb"
fi

for this_os in "${OS[@]}"; do
    for this_arch in "${ARCH[@]}"; do

        if [ "${this_arch}" == "arm64" ] && [ "${this_os}" == "linux" ]; then
            # TODO support linux arm64
            continue
        fi

        # We should only be updating homebrew with a version of telepresence that
        # already exists, so let's download it
        if [ "${PACKAGE_NAME}" == 'tel2' ]; then
            DOWNLOAD_PATH="/download/${PACKAGE_NAME}/${this_os}/${this_arch}/v${VERSION}/telepresence"
        elif [ "${PACKAGE_NAME}" == 'tel2oss' ]; then
            DOWNLOAD_PATH="/download/${PACKAGE_NAME}/releases/download/v${VERSION}/telepresence-${this_os}-${this_arch}"
        fi
        echo "Downloading ${DOWNLOAD_PATH}"
        mkdir -p "${WORK_DIR}/${this_os}/${this_arch}/"
        curl -fL "https://app.getambassador.io/${DOWNLOAD_PATH}" -o "${WORK_DIR}/${this_os}/${this_arch}/telepresence"
        declare -x "TARBALL_HASH_${this_os}_${this_arch}"="$(shasum -a 256 "${WORK_DIR}/${this_os}/${this_arch}/telepresence" | cut -f 1 -d " ")"
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

export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null

# Clone telepresenceio-homebrew:
echo "Cloning into ${BUILD_HOMEBREW_DIR}..."
if [ "${GITHUB_TOKEN}" == "" ]; then
  git clone "https://github.com/telepresenceio/homebrew-telepresence.git" "${BUILD_HOMEBREW_DIR}"
else
  git clone "https://${GITHUB_TOKEN}@github.com/telepresenceio/homebrew-telepresence.git" "${BUILD_HOMEBREW_DIR}"
fi
cd "${BUILD_HOMEBREW_DIR}"

# Update recipe
mkdir -p "$(dirname "${FORMULA}")"
cp "${FORMULA_FILE}" "${FORMULA}"

sed -i'' -e "s/__FORMULA_NAME__/${FORMULA_NAME}/g" "${FORMULA}"
sed -i'' -e "s/__NEW_VERSION__/${VERSION}/g" "${FORMULA}"

for this_os in "${OS[@]}"; do
    for this_arch in "${ARCH[@]}"; do

        if [ "${this_arch}" == "arm64" ] && [ "${this_os}" == "linux" ]; then
            # TODO support linux arm64
            continue
        fi
        tmp_var="TARBALL_HASH_${this_os}_${this_arch}"
        sed -i'' -e "s/__TARBALL_HASH_${this_os^^}_${this_arch^^}__/${!tmp_var}/g" "${FORMULA}"
    done
done

chmod 644 "${FORMULA}"

# Use the correct machine user for committing
git config --local user.email "${GITHUB_EMAIL}"
git config --local user.name "${GITHUB_USER}"

git add "${FORMULA}"
git commit -m "Release ${VERSION}"
git tag --message "Release ${VERSION}" "${VERSION}"

# This cat is just so we can see the formula in case
# the git permissions are incorrect and we can't publish
# the change. Once we know the automation is working, we can
# remove it.
cat "${FORMULA}"
git push origin "${VERSION}" main

# Clean up the working directory
rm -rf "${WORK_DIR}"
