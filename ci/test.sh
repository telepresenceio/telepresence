#!/bin/bash
set -e
virtualenv/bin/flake8 local/*.py remote/*.py cli/telepresence
# pylint doesn't work on Travis OS X, perhaps because it's python 3.6:
if [ "$(uname)" == "Linux" ]; then virtualenv/bin/pylint -f parseable -E cli/telepresence; fi
cli/telepresence --version
echo | cli/telepresence --help
if [ -z "$TELEPRESENCE_TESTS" ]; then
    [ -z "TELEPRESENCE_OPENSHIFT" ] && export TELEPRESENCE_TESTS="-n 4";
fi
env PATH="$PWD/cli/:$PATH" virtualenv/bin/py.test -v \
    --timeout 360 --timeout-method thread --fulltrace $TELEPRESENCE_TESTS tests remote/test_socks.py
