#!/bin/bash
set -e
cd /tmp
wget https://github.com/dgoulet/torsocks/archive/v2.1.0.tar.gz
tar xvfz v2.1.0.tar.gz
cd torsocks-2.1.0
./autogen.sh
./configure --prefix=/usr
make
sudo make install
