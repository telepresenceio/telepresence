#!/bin/bash

# Build script for Mac OS X automated builds

set -eEx

SCOUT_DISABLE=1
PROJECT_NAME=datawireio

cleanup() {
  printf "Performing cleanup...\n"
  kubernaut discard
}

trap cleanup ERR

# mac os x only
brew cask install osxfuse
brew install python3 sshfs torsocks

# record debugging information
python --version
python3 --version

# install
make virtualenv
make virtualenv/bin/sshuttle-telepresence

mkdir ~/tpbin
cp virtualenv/bin/sshuttle-telepresence ~/tpbin/
export PROJECT_NAME=datawireio
export TELEPRESENCE_REGISTRY=gcr.io/${PROJECT_NAME}
export TELEPRESENCE_VER_SUFFIX="-osx"
export PATH=$PATH:$HOME/google-cloud-sdk/bin:~/tpbin/

# Build and push images
./ci/push-images.sh

# Get a Kubernetes cluster
kubernaut claim
export KUBECONFIG=${HOME}/.kube/kubernaut

# Run tests
env TELEPRESENCE_METHOD=container ./ci/test.sh
env TELEPRESENCE_METHOD=inject-tcp ./ci/test.sh
env TELEPRESENCE_METHOD=vpn-tcp ./ci/test.sh

# Discard
kubernaut discard

