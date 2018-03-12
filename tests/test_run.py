"""
End-to-end tests for running directly in the operating system.
"""

import json
from pprint import pformat
from unittest import TestCase, skipIf
from subprocess import (
    check_output,
    Popen,
    PIPE,
    check_call,
    STDOUT,
)
import time
import os

from .utils import (
    DIRECTORY,
    random_name,
    run_webserver,
    telepresence_version,
    current_namespace,
    OPENSHIFT,
    KUBECTL,
    DEPLOYMENT_TYPE,
    EXISTING_DEPLOYMENT,
    query_in_k8s,
)

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")
# inject-tcp/vpn-tcp/container:
TELEPRESENCE_METHOD = os.environ.get("TELEPRESENCE_METHOD", None)
# If this env variable is set, we know we're using minikube or minishift:
LOCAL_VM = os.environ.get("TELEPRESENCE_LOCAL_VM") is not None


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
            "--method",
            TELEPRESENCE_METHOD,
            "--run-shell",
        ],
        cwd=str(DIRECTORY),
        stdin=PIPE,
    )
    p.stdin.write(bytes(local_command, "ascii") + b"\n")
    p.stdin.flush()
    p.stdin.close()
    return p.wait()


def assert_fromcluster(namespace, service_name, port, telepresence_process):
    """Assert that there's a webserver accessible from the cluster."""
    url = "http://{}:{}/__init__.py".format(service_name, port)
    print("assert_fromcluster(url={})".format(url))
    expected = (DIRECTORY / "__init__.py").read_bytes()
    for i in range(30):
        result = query_in_k8s(namespace, url, telepresence_process)
        if result != expected:
            time.sleep(1)
        else:
            break
    assert result == expected
    print("Hooray, got expected result when querying via cluster.")


@skipIf(TELEPRESENCE_METHOD == "container", "non-Docker tests")
class NativeEndToEndTests(TestCase):
    """
    End-to-end tests on the native machine.
    """

    @skipIf(OPENSHIFT, "OpenShift Online doesn't do namespaces")
    def create_namespace(self):
        """Create a new namespace, return its name."""
        name = random_name()
        yaml = NAMESPACE_YAML.format(name).encode("utf-8")
        check_output(
            args=[
                KUBECTL,
                "apply",
                "-f",
                "-",
            ],
            input=yaml,
        )
        self.addCleanup(
            lambda: check_output([KUBECTL, "delete", "namespace", name])
        )
        return name

    # TODO test default namespace behavior
    def fromcluster(
        self, telepresence_args, url, namespace, local_port, remote_port=None
    ):
        """
        Test of communication from the cluster.

        Start webserver that serves files from this directory. Run HTTP query
        against it on the Kubernetes cluster, compare to real file.
        """
        if remote_port is None:
            port_string = str(local_port)
            remote_port = local_port
        else:
            port_string = "{}:{}".format(local_port, remote_port)

        args = ["telepresence"] + telepresence_args + [
            "--expose",
            port_string,
            "--logfile",
            "-",
            "--method",
            TELEPRESENCE_METHOD,
            "--run-shell",
        ]
        p = Popen(args=args, stdin=PIPE, stderr=STDOUT, cwd=str(DIRECTORY))
        p.stdin.write(
            ("sleep 1; exec python3 -m http.server %s\n" %
             (local_port, )).encode("ascii")
        )
        p.stdin.flush()
        try:
            assert_fromcluster(namespace, url, remote_port, p)
        finally:
            p.stdin.close()
            p.terminate()
            p.wait()

    def test_disconnect(self):
        """Telepresence exits if the connection is lost."""
        exit_code = run_script_test(["--new-deployment", random_name()],
                                    "python3 disconnect.py")
        # Exit code 3 means proxy exited prematurely:
        assert exit_code == 3

    @skipIf(
        LOCAL_VM and TELEPRESENCE_METHOD == "vpn-tcp",
        "--deployment doesn't work on local VMs with vpn-tcp method."
    )
    def existingdeployment(self, namespace, script):
        if namespace is None:
            namespace = current_namespace()
        webserver_name = run_webserver(namespace)

        # Create a Deployment outside of Telepresence:
        name = random_name()
        image = "{}/telepresence-k8s:{}".format(
            REGISTRY, telepresence_version()
        )
        deployment = EXISTING_DEPLOYMENT.format(
            name=name,
            container_name=name,
            image=image,
            namespace=namespace,
            replicas="1",
        )
        check_output(
            args=[
                KUBECTL,
                "apply",
                "-f",
                "-",
            ],
            input=deployment.encode("utf-8")
        )

        def cleanup():
            check_output([
                KUBECTL, "delete", DEPLOYMENT_TYPE, name,
                "--namespace=" + namespace
            ])
            check_output([
                KUBECTL, "delete", "ConfigMap", name,
                "--namespace=" + namespace
            ])
        self.addCleanup(cleanup)

        args = ["--deployment", name, "--namespace", namespace]
        exit_code = run_script_test(
            args, "python3 {} {} {}".format(
                script,
                webserver_name,
                namespace,
            )
        )
        assert 113 == exit_code

    # XXX Test existing deployment w/ default namespace

    def test_swapdeployment_swap_args(self):
        """
        --swap-deployment swaps out Telepresence pod and overrides the entrypoint.
        """
        # Create a non-Telepresence deployment:
        name = random_name()
        check_call([
            KUBECTL,
            "run",
            name,
            "--restart=Always",
            "--image=openshift/hello-openshift",
            "--replicas=2",
            "--labels=telepresence-test=" + name,
            "--env=HELLO=there",
            "--",
            "/hello-openshift",
        ])
        self.addCleanup(check_call, [KUBECTL, "delete", DEPLOYMENT_TYPE, name])
        self.assert_swapdeployment(name, 2, "telepresence-test=" + name)

    @skipIf(not OPENSHIFT, "Only runs on OpenShift")
    def test_swapdeployment_ocnewapp(self):
        """
        --swap-deployment works on pods created via `oc new-app`.
        """
        name = random_name()
        check_call([
            "oc",
            "new-app",
            "--name=" + name,
            "--docker-image=openshift/hello-openshift",
            "--env=HELLO=there",
        ])
        self.addCleanup(
            check_call, ["oc", "delete", "dc,imagestream,service", name]
        )
        self.assert_swapdeployment(name, 1, "app=" + name)

    def assert_swapdeployment(self, name, replicas, selector):
        """
        --swap-deployment swaps out Telepresence pod and then swaps it back on
        exit.
        """
        webserver_name = run_webserver()
        p = Popen(
            args=[
                "telepresence", "--swap-deployment", name, "--logfile", "-",
                "--method", TELEPRESENCE_METHOD, "--run", "python3",
                "tocluster.py", webserver_name, current_namespace(),
                "HELLO=there"
            ],
            cwd=str(DIRECTORY),
        )
        exit_code = p.wait()
        assert 113 == exit_code
        deployment = json.loads(
            str(
                check_output([
                    KUBECTL, "get", DEPLOYMENT_TYPE, name, "-o", "json",
                    "--export"
                ]), "utf-8"
            )
        )

    def test_swapdeployment_auto_expose(self):
        """
        --swap-deployment auto-exposes ports listed in the Deployment.

        Important that the test uses port actually used by original container,
        otherwise we will miss bugs where a telepresence proxy container is
        added rather than being swapped.
        """
        service_name = random_name()
        check_call([
            KUBECTL,
            "run",
            service_name,
            "--port=8888",
            "--expose",
            "--restart=Always",
            "--image=openshift/hello-openshift",
            "--replicas=2",
            "--labels=telepresence-test=" + service_name,
            "--env=HELLO=there",
        ])
        self.addCleanup(
            check_call, [KUBECTL, "delete", DEPLOYMENT_TYPE, service_name]
        )
        port = 8888
        # Explicitly do NOT use '--expose 8888', to see if it's auto-detected:
        p = Popen(
            args=[
                "telepresence", "--swap-deployment", service_name,
                "--logfile", "-", "--method", TELEPRESENCE_METHOD,
                "--run", "python3", "-m",
                "http.server", str(port)
            ],
            cwd=str(DIRECTORY),
        )

        assert_fromcluster(current_namespace(), service_name, port, p)
        p.terminate()
        p.wait()
