#!/usr/bin/env sh
set -e
if [ $(id -u) = 0 ]; then
    echo "root -> telepresence"
    exec su telepresence ./run.sh
fi
exec ./run.sh
