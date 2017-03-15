"""
End-to-end tests for running inside a Docker container.
"""

from unittest import TestCase
from pathlib import Path
from subprocess import check_output, Popen, STDOUT
import time

DIRECTORY = Path(__file__).absolute().parent


def random_name():
    """Return a new name each time."""
    return "testing-{}".format(time.time()).replace(".", "-")


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
        def get_docker_ps():
            return set(check_output(["sudo", "docker", "ps", "-q"]).strip().split())

        docker_processes = get_docker_ps()

        result = str(
            check_output([
                "telepresence",
                "--new-deployment",
                "tests",
                "--docker-run",
                "-v",
                "{}:/code".format(DIRECTORY),
                "--rm",
                "python:3.5-slim",
                "python",
                "/code/tocluster.py",
            ]), "utf-8")
        assert "SUCCESS!" in result
        # Shouldn't leave any Docker processes behind:
        assert get_docker_ps() == docker_processes

    def test_fromcluster(self):
        """
        Tests of communication from the cluster.

        Start webserver that serves files from this directory. Run HTTP query
        against it on the Kubernetes cluster, compare to real file.
        """
        p = Popen(
            [
                "telepresence", "--new-deployment", "fromclustertests",
                "--expose", "8080", "--docker-run", "-v",
                "{}:/code".format(DIRECTORY), "--rm", "-w", "/code",
                "python:3.5-slim", "python3", "-m", "http.server", "8080"
            ], )

        def cleanup():
            p.terminate()
            p.wait()

        self.addCleanup(cleanup)
        time.sleep(30)
        result = check_output([
            'kubectl', 'run', '--attach', random_name(),
            '--generator=job/v1',
            "--quiet", '--rm', '--image=alpine', '--restart', 'Never',
            '--command', '--', '/bin/sh', '-c',
            "apk add --no-cache --quiet curl && " +
            "curl http://fromclustertests:8080/__init__.py"
        ])
        assert result == (DIRECTORY / "__init__.py").read_bytes()

    def test_existingdeployment(self):
        """
        Tests of communicating with existing Deployment.
        """
        name = random_name()
        version = str(
            check_output(["telepresence", "--version"], stderr=STDOUT),
            "utf-8").strip()
        check_output([
            "kubectl",
            "run",
            "--generator",
            "deployment/v1beta1",
            name,
            "--image=datawire/telepresence-k8s:" + version,
            '--env="MYENV=hello"',
        ])
        self.addCleanup(check_output,
                        ["kubectl", "delete", "deployment", name])
        result = str(
            check_output([
                "telepresence",
                "--deployment",
                name,
                "--docker-run",
                "-v",
                "{}:/code".format(DIRECTORY),
                "--rm",
                "python:3.5-slim",
                "python",
                "/code/tocluster.py",
                "MYENV=hello",
            ]), "utf-8")
        assert "SUCCESS!" in result
