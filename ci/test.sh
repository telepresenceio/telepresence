#!/bin/bash
set -e
virtualenv/bin/flake8 local-docker/*.py k8s-proxy/*.py cli/telepresence
# pylint doesn't work on Travis OS X, perhaps because it's python 3.6:
if [ "$(uname)" == "Linux" ]; then virtualenv/bin/pylint -f parseable -E cli/telepresence; fi
cli/telepresence --version
echo | cli/telepresence --help
if [ -z "$TELEPRESENCE_TESTS" ]; then
    # Don't want parallism for OpenShift (causes problems with OpenShift
    # Online's limited free plan) and don't want parallism for VPN-y method
    # since should only have one running a time.
    [ -z "$TELEPRESENCE_OPENSHIFT" ] && [ "$TELEPRESENCE_METHOD" != "vpn-tcp" ] && export TELEPRESENCE_TESTS="-n 4";
fi
env PATH="$PWD/cli/:$PATH" virtualenv/bin/py.test -v \
    --timeout 360 --timeout-method thread --fulltrace $TELEPRESENCE_TESTS tests k8s-proxy/test_socks.py
