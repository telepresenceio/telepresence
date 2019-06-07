#!/bin/bash

set -o errexit
set -o pipefail
set -o nounset
# set -o xtrace

OS=$(python3 -c "import sys; print(sys.platform)")

echo "Check for requirements"
uuidgen > /dev/null
curl -V > /dev/null
echo "$KUBERNAUT_TOKEN" > /dev/null

echo "Set up ${OS}-specific stuff"
case "${OS}" in
    osx)
        brew update > /dev/null
        brew install python3 || brew upgrade python
        brew cask install osxfuse
        brew install sshfs
        pip3 install virtualenv
        ;;

    linux)
        sudo apt-get install sshfs conntrack lsb-release
        ;;

    *)
        echo "Unknown platform."
        exit 1
esac

echo "Download commands"
curl -sLO "https://storage.googleapis.com/kubernetes-release/release/v1.12.2/bin/${OS}/amd64/kubectl"
curl -sLO "http://releases.datawire.io/kubernaut/$(curl -s http://releases.datawire.io/kubernaut/latest.txt)/${OS}/amd64/kubernaut"

echo "Install commands"
chmod a+x kubectl kubernaut
sudo mv kubectl kubernaut /usr/local/bin

echo "Install torsocks"
./ci/build-torsocks.sh "$OS"

echo "Set up kubernaut's backend"
mkdir -p ~/.config/kubernaut
kubernaut config backend create --url="https://next.kubernaut.io" --name="v2" --activate "$KUBERNAUT_TOKEN"

echo "Claim a cluster"
echo "tel-$(uuidgen)" > /tmp/kubernaut-claim-name.txt
kubernaut claims create --name "$(cat /tmp/kubernaut-claim-name.txt)" --cluster-group main
