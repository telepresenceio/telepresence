#!/bin/bash
set -ex

virtualenv/bin/yapf -dr telepresence > /dev/null || { echo "YAPF check failed" >&2; exit 1; }

virtualenv/bin/flake8 --isolated local-docker k8s-proxy telepresence

# pylint doesn't work on Travis OS X, perhaps because it's python 3.6:
if [ "$TRAVIS_OS_NAME" != "osx" ]; then
    virtualenv/bin/pylint -f parseable -E telepresence
fi

virtualenv/bin/mypy telepresence local-docker/entrypoint.py

# Couldn't figure out how to make this work well, so it's not very useful cause
# of the skip:
virtualenv/bin/mypy --ignore-missing-imports k8s-proxy/forwarder.py k8s-proxy/socks.py

telepresence --help
