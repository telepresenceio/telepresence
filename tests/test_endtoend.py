"""
End-to-end tests.

Theory of operation: Python scripts are run using telepresence --docker-run,
and their output is checked for the string "SUCCESS!" indicating the checks
passed. The scripts shouldn't use code py.test would detect as a test.
"""

from pathlib import Path
from subprocess import check_output

DIRECTORY = Path(__file__).absolute().parent


def run(filename):
    """Run a Python file using Telepresence."""
    result = check_output([
        "telepresence", "--new-deployment", "tests", "--docker-run", "-v",
        "{}:/code".format(DIRECTORY), "--rm", "python:3.5-slim", "python",
        "/code/" + filename
    ])
    assert "SUCCESS!" in str(result, "utf-8")


def test_tocluster():
    """Tests of communication to the cluster."""
    run("tocluster.py")
