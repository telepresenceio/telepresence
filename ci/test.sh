#!/bin/bash
set -ex
if [ -z "$TELEPRESENCE_TESTS" ]; then
    # Don't want parallism for OpenShift (causes problems with OpenShift
    # Online's limited free plan) and don't want parallelism for VPN-y method
    # since should only have one running a time, and parallelism breaks container
    # method on OS X.
    [ -z "$TELEPRESENCE_OPENSHIFT" ] && [ "$TELEPRESENCE_METHOD" == "inject-tcp" ] && export TELEPRESENCE_TESTS="-n 4";
fi
env PATH="$PWD/cli/:$PATH" virtualenv/bin/py.test -v \
    --timeout 360 --timeout-method thread --fulltrace $TELEPRESENCE_TESTS tests k8s-proxy/test_socks.py
