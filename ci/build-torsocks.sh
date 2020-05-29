#!/bin/bash
set -e

OS=$1

if [[ "$OS" == "osx" ]]; then
    brew install torsocks;
fi

if [[ "$OS" == "linux" ]]; then
    apt-get -qq install torsocks
fi
