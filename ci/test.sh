#!/bin/bash
set -e
virtualenv/bin/flake8 local/*.py remote/*.py cli/telepresence
cli/telepresence --version
cli/telepresence --help
env PATH=$PWD/cli/:$PATH virtualenv/bin/py.test -v -s --fulltrace tests remote/test_socks.py
