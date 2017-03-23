"""
End-to-end tests for running directly in the operating system.
"""

from unittest import TestCase
from subprocess import check_output, Popen, PIPE
import time

from .utils import DIRECTORY, random_name


class EndToEndTests(TestCase):
    """
    End-to-end tests.
    """

    def test_tocluster(self):
        """
        Tests of communication to the cluster.

        Python script is run using telepresence --run, and output is
        checked for the string "SUCCESS!" indicating the checks passed. The
        script shouldn't use code py.test would detect as a test.
        """
        p = Popen(
            args=[
                "telepresence",
                "--new-deployment",
                random_name(),
                "--logfile", "-",
                "--run-shell",
            ],
            cwd=str(DIRECTORY),
            stdin=PIPE,
        )
        p.stdin.write(b"python3 tocluster.\y\n")
        p.stdin.flush()
        p.stdin.close()
        exit_code = p.wait()
        assert exit_code == 0

    def test_fromcluster(self):
        """
        Tests of communication from the cluster.

        Start webserver that serves files from this directory. Run HTTP query
        against it on the Kubernetes cluster, compare to real file.
        """
        name = random_name()
        p = Popen(
            args=[
                "telepresence",
                "--new-deployment",
                name,
                "--expose",
                "12345",
                "--logfile", "-",
                "--run-shell",
            ],
            stdin=PIPE,
            cwd=str(DIRECTORY)
        )
        p.stdin.write(b"python3 -m http.server 12345\n")
        p.stdin.flush()

        def cleanup():
            p.terminate()
            p.wait()

        self.addCleanup(cleanup)
        time.sleep(60)
        result = check_output([
            'kubectl', 'run', '--attach', random_name(), '--generator=job/v1',
            "--quiet", '--rm', '--image=alpine', '--restart', 'Never',
            '--command', '--', '/bin/sh', '-c',
            "apk add --no-cache --quiet curl && " +
            "curl http://{}:12345/__init__.py".format(name)
        ])
        assert result == (DIRECTORY / "__init__.py").read_bytes()

    # XXX write test for IP-based routing, not just DNS-based routing!
