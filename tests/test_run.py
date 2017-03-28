"""
End-to-end tests for running directly in the operating system.
"""

from unittest import TestCase
from subprocess import check_output, Popen, PIPE
import atexit
import time

from .utils import DIRECTORY, random_name


def run_script_test(script):
    """Run a script with Telepresence."""
    p = Popen(
        args=[
            "telepresence",
            "--new-deployment",
            random_name(),
            "--logfile",
            "-",
            "--run-shell",
        ],
        cwd=str(DIRECTORY),
        stdin=PIPE,
    )
    p.stdin.write(b"python3 %.py\n" % (script,))
    p.stdin.flush()
    p.stdin.close()
    return p.wait()


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
        exit_code = run_script_test(b"tocluster.py")
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
                "--logfile",
                "-",
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
            "curl --silent http://{}:12345/__init__.py".format(name)
        ])
        assert result == (DIRECTORY / "__init__.py").read_bytes()

    def test_loopback(self):
        """The shell run by telepresence can access localhost."""
        p = Popen(["python3", "-m", "http.server", "12346"],
                  cwd=str(DIRECTORY))
        atexit.register(p.terminate)

        name = random_name()
        p = Popen(
            args=[
                "telepresence",
                "--new-deployment",
                name,
                "--run-shell",
            ],
            stdin=PIPE,
            stdout=PIPE,
            cwd=str(DIRECTORY)
        )
        result, _ = p.communicate(
            b"curl --silent http://localhost:12346/test_run.py\n"
        )
        # We're loading this file via curl, so it should have the string
        # "cuttlefish" which is in this comment and unlikely to appear by
        # accident.
        assert b"cuttlefish" in result

    def test_disconnect(self):
        """Telepresence exits if the connection is lost."""
        exit_code = run_script_test(b"disconnect.py")
        # Exit code 3 means proxy exited prematurely:
        assert exit_code == 3

    def test_proxy(self):
        """Telepresence proxies all connections via the cluster."""
        exit_code = run_script_test(b"proxy.py")
        assert exit_code == 0

    # XXX write test for IP-based routing, not just DNS-based routing!
