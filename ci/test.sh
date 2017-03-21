#!/bin/bash
virtualenv/bin/flake8 local/*.py remote/*.py cli/telepresence
env PATH=$PWD/cli/:$PATH virtualenv/bin/py.test -s --fulltrace tests remote/test_socks.py
