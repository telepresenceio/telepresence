"""
End-to-end tests.
"""

from unittest import TestCase
from pathlib import Path
from subprocess import check_output, Popen
import time

DIRECTORY = Path(__file__).absolute().parent


def run(filename, extra_telepresence_args=[]):
    """Run a Python file using Telepresence."""
    return str(
        check_output([
            "telepresence", "--new-deployment", "tests"
        ] + extra_telepresence_args + [
            "--docker-run", "-v", "{}:/code".format(DIRECTORY), "--rm",
            "python:3.5-slim", "python", "/code/" + filename
        ]), "utf-8")


class EndToEndTests(TestCase):
    """
    End-to-end tests.

    Only reason I'm using this is that I can't figure equivalent of addCleanup
    in py.test.
    """

    def test_tocluster(self):
        """
        Tests of communication to the cluster.

        Python script is run using telepresence --docker-run, and output is
        checked for the string "SUCCESS!" indicating the checks passed. The
        script shouldn't use code py.test would detect as a test.
        """
        result = run("tocluster.py")
        assert "SUCCESS!" in result

    def test_fromcluster(self):
        """
        Tests of communication from the cluster.

        Start webserver that serves files from this directory. Run HTTP query
        against it on the Kubernetes cluster, compare to real file.
        """
        # XXX leaking docker processes, try to figure out why
        p = Popen(
            [
                "telepresence", "--new-deployment", "fromclustertests",
                "--expose", "8080", "--docker-run", "-v",
                "{}:/code".format(DIRECTORY), "--rm", "-w", "/code",
                "python:3.5-slim", "python3", "-m", "http.server", "8080"
            ], )
        self.addCleanup(p.terminate)
        time.sleep(30)
        result = check_output([
            'kubectl', 'run', '--attach', 'testing123', '--generator=job/v1',
            "--quiet", '--rm', '--image=alpine', '--restart', 'Never',
            '--command', '--', '/bin/sh', '-c',
            "apk add --no-cache --quiet curl && " +
            "curl http://fromclustertests:8080/test_endtoend.py"
        ])
        assert result == (DIRECTORY / "test_endtoend.py").read_bytes()
