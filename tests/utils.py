"""Utilities."""

from pathlib import Path
import time

DIRECTORY = Path(__file__).absolute().parent


def random_name():
    """Return a new name each time."""
    return "testing-{}".format(time.time()).replace(".", "-")
