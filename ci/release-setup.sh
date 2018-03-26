#!/bin/sh
# Prepare for release in an Alpine container
# docker run --rm -it --name build -v /var/run/docker.sock:/var/run/docker.sock alpine:3.7 sh
# docker cp release-setup.sh build:/root/

set -eu

apk --no-cache add bash build-base ca-certificates docker git openssh-client python python3-dev ruby ruby-dev
pip3 install awscli
gem install --no-document package_cloud

cd
wget https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-sdk-194.0.0-linux-x86_64.tar.gz
tar xf google-cloud-sdk-194.0.0-linux-x86_64.tar.gz
./google-cloud-sdk/install.sh --quiet

git config --global user.email "services@datawire.io"
git config --global user.name "d6e automaton"
