#!/bin/bash
set -e

OS=$1

if [[ "$OS" == "osx" ]]; then
    brew install torsocks;
fi

if [[ "$OS" == "linux" ]]; then
    sudo apt-get install -y coreutils
    PATCH="$(realpath $(dirname $0))/torsocks-h_addrtype.patch"
    cd /tmp
    wget https://github.com/dgoulet/torsocks/archive/v2.1.0.tar.gz
    tar xvfz v2.1.0.tar.gz
    cd torsocks-2.1.0
    ./autogen.sh
    ./configure --prefix=/usr
    patch -p1 <"${PATCH}"
    make
    sudo make install;
fi
