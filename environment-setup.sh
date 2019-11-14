#!/bin/sh

# Prepare a development environment for running telepresence and its test
# suite.  These steps should typically only be required once to prepare the
# environment.

if [ "$#" -ne 4 ]; then
    echo "Usage: $0 <gcloud project name> <gcloud cluster name> <gcloud compute zone> <linux|osx>"
    echo "  (See .circleci/config.yml for sample values)"
    exit 1
fi

set -ex

PROJECT_NAME=$1
CLUSTER_NAME=$2
CLOUDSDK_COMPUTE_ZONE=$3
OS=$4

case "${OS}" in
    osx)
        brew update > /dev/null
        brew install python3 || brew upgrade python
        brew cask install osxfuse
        brew install sshfs
        pip3 install virtualenv
        curl -Ls -o /usr/local/bin/edgectl https://metriton.datawire.io/downloads/darwin/edgectl
        chmod a+x /usr/local/bin/edgectl
        ;;

    linux)
        sudo apt-get install \
             sshfs conntrack \
             lsb-release
        curl -Ls -o /tmp/edgectl https://metriton.datawire.io/downloads/linux/edgectl
        sudo mv /tmp/edgectl /usr/bin/edgectl
        sudo chmod a+x /usr/bin/edgectl
        ;;

    *)
        echo "Unknown platform."
        exit 1
esac

# Record some debugging info:
python --version
python2 --version || true
python3 --version
ruby --version || true
docker version || true

sudo edgectl daemon
edgectl version || true

# Make sure gcloud is installed.  This includes kubectl.
./ci/setup-gcloud.sh "${PROJECT_NAME}" "${CLUSTER_NAME}" "${CLOUDSDK_COMPUTE_ZONE}" "${OS}"

# Make sure torsocks is installed:
./ci/build-torsocks.sh "${OS}"

# Make sure Docker sshfs plugin is installed.
docker plugin install --grant-all-permissions vieux/sshfs || true
