"""
End-to-end tests for running inside a Docker container.
"""

import atexit
from unittest import TestCase
from subprocess import check_output, Popen, check_call
import time
import os

from .utils import DIRECTORY, random_name, telepresence_version, run_nginx

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")

EXISTING_DEPLOYMENT = """\
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: {name}
spec:
  replicas: 1
  template:
    metadata:
      labels:
        name: {name}
    spec:
      containers:
      # Extra container at start to demonstrate we can handle multiple
      # containers
      - name: getintheway
        image: nginx:alpine
      - name: {name}
        image: {registry}/telepresence-k8s:{version}
        env:
        - name: MYENV
          value: hello
"""


class EndToEndTests(TestCase):
    """
    End-to-end tests.

    Only reason I'm using this is that I can't figure equivalent of addCleanup
    in py.test.
    """

    def test_tocluster(self):
        """
        Tests of communication to the cluster.

        Python script is run using telepresence --docker-run, and 0 exit code
        indicates success.
        """

        def get_docker_ps():
            return set(
                check_output(["sudo", "docker", "ps", "-q"]).strip().split()
            )

        docker_processes = get_docker_ps()

        check_call([
            "telepresence",
            "--new-deployment",
            random_name(),
            "--logfile",
            "-",
            "--docker-run",
            "-v",
            "{}:/code".format(DIRECTORY),
            "--rm",
            "python:3.5-slim",
            "python",
            "/code/tocluster.py",
        ])

        # Shouldn't leave any Docker processes behind:
        assert get_docker_ps() == docker_processes

    def test_fromcluster(self):
        """
        Tests of communication from the cluster.

        Start webserver that serves files from this directory. Run HTTP query
        against it on the Kubernetes cluster, compare to real file.
        """
        name = random_name()
        p = Popen(
            [
                "telepresence", "--new-deployment", name, "--logfile", "-",
                "--expose", "8080", "--docker-run", "-v",
                "{}:/code".format(DIRECTORY), "--rm", "-w", "/code",
                "python:3.5-slim", "python3", "-m", "http.server", "8080"
            ],
        )

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
            "curl --silent http://{}:8080/__init__.py".format(name)
        ])
        assert result == (DIRECTORY / "__init__.py").read_bytes()

    def test_existingdeployment(self):
        """
        Tests of communicating with existing Deployment.
        """
        name = random_name()
        deployment = EXISTING_DEPLOYMENT.format(
            name=name, registry=REGISTRY, version=telepresence_version()
        )
        check_output(
            args=[
                "kubectl",
                "apply",
                "-f",
                "-",
            ],
            input=deployment.encode("utf-8")
        )
        self.addCleanup(
            check_output, ["kubectl", "delete", "deployment", name]
        )
        check_call([
            "telepresence",
            "--deployment",
            name,
            "--logfile",
            "-",
            "--docker-run",
            "-v",
            "{}:/code".format(DIRECTORY),
            "--rm",
            "python:3.5-slim",
            "python",
            "/code/tocluster.py",
            "MYENV=hello",
        ])

    def test_proxy(self):
        """Telepresence proxies connections set with --proxy."""
        nginx_name = run_nginx()

        # The telepresence-local image is handy insofar as it has kubectl and
        # python3. We override its entroypoint, though.
        home = os.path.expanduser("~")
        check_call([
            "telepresence",
            "--new-deployment",
            random_name(),
            "--logfile",
            "-",
            "--proxy",
            "analiaswedefine:80",
            "--docker-run",
            "-v",
            "{}:/code".format(DIRECTORY),
            "-v",
            home + ":/opt",
            "-v",
            home + ":" + home,
            "--rm",
            "--entrypoint=python3",
            "{}/telepresence-local:{}".format(
                REGISTRY, telepresence_version()
            ),
            "/code/proxy.py",
            nginx_name,
        ])
