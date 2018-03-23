#!/bin/bash
set -e

OS=$1

if [[ "$OS" == "osx" ]]; then
    brew install torsocks;
fi

if [[ "$OS" == "linux" ]]; then
    type -p dnf && sudo dnf install torsocks \
    || type -p apt-get && sudo apt-get install -y
fi
