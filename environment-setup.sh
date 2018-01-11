#!/bin/sh -ex

# Prepare a development environment for running telepresence and its test
# suite.  These steps should typically only be required once to prepare the
# environment.

if [ "$#" -ne 4 ]; then
    echo "Usage: $0 <gcloud project name> <gcloud cluster name> <gcloud compute zone>"
    echo "  (See .travis.yml for sample values)"
    exit 1
fi

PROJECT_NAME=$1
CLUSTER_NAME=$2
CLOUDSDK_COMPUTE_ZONE=$3
OS=$4

case "$(uname -s)" in
    Darwin)
        brew update > /dev/null
        brew cask install osxfuse
        brew install python3 sshfs
        ;;

    Linux)
        sudo apt install sshfs conntrack python3
        ;;

    *)
        echo "Unknown platform."
        exit 1
esac

# Record some debugging info:
python --version
python2 --version
python3 --version
ruby --version

# Make sure gcloud is installed.  This includes kubectl.
./ci/setup-gcloud.sh "${PROJECT_NAME}" "${CLUSTER_NAME}" "${CLOUDSDK_COMPUTE_ZONE}"

# Make sure torsocks is installed:
./ci/build-torsocks.sh "$OS"
