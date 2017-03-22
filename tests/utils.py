"""Utilities."""

from pathlib import Path
import time
from subprocess import check_output

DIRECTORY = Path(__file__).absolute().parent
REVISION = str(check_output(["git", "rev-parse", "--short", "HEAD"]), "utf-8"
               ).strip()


def random_name():
    """Return a new name each time."""
    return "testing-{}-{}".format(REVISION, time.time()).replace(".", "-")
