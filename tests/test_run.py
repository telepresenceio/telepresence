"""
End-to-end tests for running directly in the operating system.
"""

from unittest import TestCase
from subprocess import check_output, Popen, PIPE
import time
import os

from .utils import DIRECTORY, random_name, run_nginx, telepresence_version

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")

EXISTING_DEPLOYMENT = """\
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: {name}
  namespace: {namespace}
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

NAMESPACE_YAML = """\
apiVersion: v1
kind: Namespace
metadata:
  name: {}
"""


def run_script_test(telepresence_args, local_command):
    """Run a script with Telepresence."""
    p = Popen(
        args=["telepresence"] + telepresence_args + [
            "--logfile",
            "-",
            "--run-shell",
        ],
        cwd=str(DIRECTORY),
        stdin=PIPE,
    )
    p.stdin.write(bytes(local_command, "ascii") + b"\n")
    p.stdin.flush()
    p.stdin.close()
    return p.wait()


class EndToEndTests(TestCase):
    """
    End-to-end tests.
    """

    def create_namespace(self):
        """Create a new namespace, return its name."""
        name = random_name()
        yaml = NAMESPACE_YAML.format(name).encode("utf-8")
        check_output(
            args=[
                "kubectl",
                "apply",
                "-f",
                "-",
            ],
            input=yaml,
        )
        self.addCleanup(
            lambda: check_output(["kubectl", "delete", "namespace", name])
        )
        return name

    def test_tocluster(self):
        """
        Tests of communication to the cluster.
        """
        nginx_name = run_nginx("default")
        time.sleep(30)  # kubernetes is speedy
        exit_code = run_script_test(
            ["--new-deployment", random_name()],
            "python3 tocluster.py {} default".format(nginx_name),
        )
        assert exit_code == 0

    def test_tocluster_with_namespace(self):
        """
        Tests of communication to the cluster with non-default namespace.
        """
        namespace = self.create_namespace()
        nginx_name = run_nginx(namespace)
        time.sleep(30)  # kubernetes is speedy
        exit_code = run_script_test(
            ["--new-deployment", random_name(), "--namespace", namespace],
            "python3 tocluster.py {} {}".format(nginx_name, namespace),
        )
        assert exit_code == 0

    def fromcluster(self, telepresence_args, url, namespace):
        """
        Test of communication from the cluster.

        Start webserver that serves files from this directory. Run HTTP query
        against it on the Kubernetes cluster, compare to real file.
        """
        p = Popen(
            args=["telepresence"] + telepresence_args + [
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
            "--namespace", namespace, '--command', '--', '/bin/sh', '-c',
            "apk add --no-cache --quiet curl && " +
            "curl --silent http://{}:12345/__init__.py".format(url)
        ])
        assert result == (DIRECTORY / "__init__.py").read_bytes()

    def test_fromcluster(self):
        """
        Communicate from the cluster to Telepresence, with default namespace.
        """
        service_name = random_name()
        self.fromcluster(
            ["--new-deployment", service_name],
            service_name,
            "default",
        )

    def test_fromcluster_with_namespace(self):
        """
        Communicate from the cluster to Telepresence, with custom namespace.
        """
        namespace = self.create_namespace()
        service_name = random_name()
        self.fromcluster(
            ["--new-deployment", service_name, "--namespace", namespace],
            "{}.{}.svc.default.local".format(service_name, namespace),
            namespace,
        )

    def test_loopback(self):
        """The shell run by telepresence can access localhost."""
        p = Popen(["python3", "-m", "http.server", "12346"],
                  cwd=str(DIRECTORY))

        def cleanup():
            p.terminate()
            p.wait()

        self.addCleanup(cleanup)

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
        exit_code = run_script_test(["--new-deployment", random_name()],
                                    "python3 disconnect.py")
        # Exit code 3 means proxy exited prematurely:
        assert exit_code == 3

    def test_proxy(self):
        """Telepresence proxies all connections via the cluster."""
        nginx_name = run_nginx("default")
        time.sleep(30)  # kubernetes is speedy
        exit_code = run_script_test(["--new-deployment", random_name()],
                                    "python3 proxy.py %s" %
                                    (nginx_name.encode("utf-8"), ))
        assert exit_code == 0

    def existingdeployment(self, namespace):
        nginx_name = run_nginx("default")
        time.sleep(30)  # kubernetes is speedy

        # Create a Deployment outside of Telepresence:
        name = random_name()
        deployment = EXISTING_DEPLOYMENT.format(
            name=name,
            registry=REGISTRY,
            version=telepresence_version(),
            namespace=namespace,
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
            check_output, [
                "kubectl", "delete", "deployment", name,
                "--namespace=" + namespace
            ]
        )

        args = ["--deployment", name]
        if namespace != "default":
            args.extend(["--namespace", namespace])
        exit_code = run_script_test(
            args, "python3 tocluster.py {} {} MYENV=hello".format(
                nginx_name, namespace
            )
        )
        assert 0 == exit_code

    def test_existingdeployment(self):
        """
        Tests of communicating with existing Deployment.
        """
        self.existingdeployment("default")

    def test_existingdeployment_custom_namespace(self):
        """
        Tests of communicating with existing Deployment in a custom namespace.
        """
        self.existingdeployment(self.create_namespace())

    # XXX write test for IP-based routing, not just DNS-based routing!
