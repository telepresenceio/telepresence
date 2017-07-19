"""
Test environment variable being set.

This module will indicate success it will exit with code 113.
"""

import os
import sys
from traceback import print_exception


def handle_error(type, value, traceback):
    print_exception(type, value, traceback, file=sys.stderr)
    raise SystemExit(3)


def check_custom_env():
    assert os.environ["MYENV"] == "hello"
    assert os.environ["EXAMPLE_ENVFROM"] == "foobar"


def main():
    # make sure exceptions cause exit:
    sys.excepthook = handle_error

    check_custom_env()

    # Exit with code indicating success:
    sys.exit(113)


if __name__ == '__main__':
    main()
