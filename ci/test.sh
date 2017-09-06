#!/bin/bash
set -ex
virtualenv/bin/flake8 local-docker/*.py k8s-proxy/*.py cli/telepresence cli/stamp-telepresence
# pylint doesn't work on Travis OS X, perhaps because it's python 3.6:
if [ "$(uname)" == "Linux" ]; then virtualenv/bin/pylint -f parseable -E cli/telepresence cli/stamp-telepresence; fi
# MYPYPATH is stupid hack to get a telepresence.py for entrypoint.py to import:
MYPYPATH=tests/ virtualenv/bin/mypy cli/telepresence local-docker/entrypoint.py
# Couldn't figure out how to make this work well, so it's not very useful cause
# of the skip:
virtualenv/bin/mypy --ignore-missing-imports k8s-proxy/forwarder.py k8s-proxy/socks.py
virtualenv/bin/mypy cli/stamp-telepresence
echo | cli/telepresence --help
if [ -z "$TELEPRESENCE_TESTS" ]; then
    # Don't want parallism for OpenShift (causes problems with OpenShift
    # Online's limited free plan) and don't want parallelism for VPN-y method
    # since should only have one running a time, and parallelism breaks container
    # method on OS X.
    [ -z "$TELEPRESENCE_OPENSHIFT" ] && [ "$TELEPRESENCE_METHOD" == "inject-tcp" ] && export TELEPRESENCE_TESTS="-n 4";
fi
env PATH="$PWD/cli/:$PATH" virtualenv/bin/py.test -v \
    --timeout 360 --timeout-method thread --fulltrace $TELEPRESENCE_TESTS tests k8s-proxy/test_socks.py
