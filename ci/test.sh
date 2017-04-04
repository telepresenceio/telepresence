#!/bin/bash
set -e
virtualenv/bin/flake8 local/*.py remote/*.py cli/telepresence
virtualenv/bin/pylint -E local/entrypoint.py
cli/telepresence --version
cli/telepresence --help
[ -z "$TELEPRESENCE_TESTS" ] && export TELEPRESENCE_TESTS="tests remote/test_socks.py"
env PATH=$PWD/cli/:$PATH virtualenv/bin/py.test \
    -v -n 4 --timeout 180 --timeout-method signal --fulltrace $TELEPRESENCE_TESTS
