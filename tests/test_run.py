"""
End-to-end tests for running directly in the operating system.
"""

import json
from unittest import TestCase, skipIf, skipUnless
from subprocess import (
    check_output,
    Popen,
    PIPE,
    CalledProcessError,
    check_call,
    run,
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
)

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")
# inject-tcp/vpn-tcp/container:
TELEPRESENCE_METHOD = os.environ["TELEPRESENCE_METHOD"]

EXISTING_DEPLOYMENT = """\
metadata:
  name: {name}
  namespace: {namespace}
spec:
  replicas: {replicas}
  template:
    metadata:
      labels:
        name: {name}
        hello: monkeys  # <-- used by volumes test
    spec:
      containers:
      # Extra container at start to demonstrate we can handle multiple
      # containers
      - name: getintheway
        image: openshift/hello-openshift
        resources:
          limits:
            memory: "150Mi"
      - name: {container_name}
        image: {image}
        env:
        - name: MYENV
          value: hello
        volumeMounts:
        - name: podinfo
          mountPath: /podinfo
        resources:
          requests:
            memory: "150Mi"
          limits:
            memory: "150Mi"
      volumes:
      - name: podinfo
        downwardAPI:
          items:
            - path: "labels"
              fieldRef:
                fieldPath: metadata.labels
"""

if OPENSHIFT:
    EXISTING_DEPLOYMENT = """\
apiVersion: v1
kind: DeploymentConfig
""" + EXISTING_DEPLOYMENT
    DEPLOYMENT_TYPE = "deploymentconfig"
else:
    EXISTING_DEPLOYMENT = """\
apiVersion: extensions/v1beta1
kind: Deployment
""" + EXISTING_DEPLOYMENT
    DEPLOYMENT_TYPE = "deployment"

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


def assert_fromcluster(namespace, url, port):
    """Assert that there's a webserver accessible from the cluster."""
    for i in range(120):
        try:
            result = check_output([
                'kubectl', 'run', '--attach', random_name(), "--quiet", '--rm',
                '--image=alpine', '--restart', 'Never', "--namespace",
                namespace, '--command', '--', '/bin/sh', '-c',
                "apk add --no-cache --quiet curl && " +
                "curl --silent --max-time 3 " +
                "http://{}:{}/__init__.py".format(url, port)
            ])
            assert result == (DIRECTORY / "__init__.py").read_bytes()
            print("Hooray, got expected result when querying via cluster.")
            return
        except CalledProcessError as e:
            print("curl failed, retrying ({})".format(e))
            time.sleep(1)
            continue
    raise RuntimeError("failed to connect to local HTTP server")


@skipIf(TELEPRESENCE_METHOD == "container", "non-Docker tests")
class NativeEndToEndTests(TestCase):
    """
    End-to-end tests on the native machine.
    """

    def test_run_directly(self):
        """--run runs the command directly."""
        webserver_name = run_webserver()
        p = Popen(
            args=[
                "telepresence",
                "--method",
                TELEPRESENCE_METHOD,
                "--new-deployment",
                random_name(),
                "--logfile",
                "-",
                "--run",
                "python3",
                "tocluster.py",
                webserver_name,
                current_namespace(),
            ],
            cwd=str(DIRECTORY),
        )
        exit_code = p.wait()
        assert exit_code == 113

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

    def test_tocluster(self):
        """
        Tests of communication to the cluster.
        """
        webserver_name = run_webserver()
        exit_code = run_script_test(
            ["--new-deployment", random_name()],
            "python3 tocluster.py {} {}".format(
                webserver_name, current_namespace()
            ),
        )
        assert exit_code == 113

    def test_tocluster_with_namespace(self):
        """
        Tests of communication to the cluster with non-default namespace.
        """
        namespace = self.create_namespace()
        webserver_name = run_webserver(namespace)
        exit_code = run_script_test(
            ["--new-deployment", random_name(), "--namespace", namespace],
            "python3 tocluster.py {} {}".format(webserver_name, namespace),
        )
        assert exit_code == 113

    @skipIf(
        OPENSHIFT, "OpenShift doesn't allow root, which the tests need "
        "(at the moment, this is fixable)"
    )
    def fromcluster(self, telepresence_args, url, namespace, port):
        """
        Test of communication from the cluster.

        Start webserver that serves files from this directory. Run HTTP query
        against it on the Kubernetes cluster, compare to real file.
        """
        p = Popen(
            args=["telepresence"] + telepresence_args + [
                "--expose",
                str(port),
                "--logfile",
                "-",
                "--method",
                TELEPRESENCE_METHOD,
                "--run-shell",
            ],
            stdin=PIPE,
            stderr=PIPE,
            cwd=str(DIRECTORY)
        )
        p.stdin.write(("exec python3 -m http.server %s\n" %
                       (port, )).encode("ascii"))
        p.stdin.flush()

        def cleanup():
            p.stdin.close()
            p.terminate()
            p.wait()

        self.addCleanup(cleanup)
        assert_fromcluster(namespace, url, port)

    def test_fromcluster(self):
        """
        Communicate from the cluster to Telepresence, with default namespace.
        """
        service_name = random_name()
        self.fromcluster(
            ["--new-deployment", service_name],
            service_name,
            current_namespace(),
            12349,
        )

    def test_fromcluster_with_namespace(self):
        """
        Communicate from the cluster to Telepresence, with custom namespace.
        """
        namespace = self.create_namespace()
        service_name = random_name()
        self.fromcluster(
            ["--new-deployment", service_name, "--namespace", namespace],
            "{}.{}.svc.cluster.local".format(service_name, namespace),
            namespace,
            12347,
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
                "--method",
                TELEPRESENCE_METHOD,
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
        self.addCleanup(
            check_output, [
                KUBECTL, "delete", DEPLOYMENT_TYPE, name,
                "--namespace=" + namespace
            ]
        )

        args = ["--deployment", name, "--namespace", namespace]
        exit_code = run_script_test(
            args, "python3 {} {} {} MYENV=hello".format(
                script,
                webserver_name,
                namespace,
            )
        )
        assert 113 == exit_code

    def test_existingdeployment(self):
        """
        Tests of communicating with existing Deployment.
        """
        self.existingdeployment(None, "tocluster.py")

    def test_existingdeployment_custom_namespace(self):
        """
        Tests of communicating with existing Deployment in a custom namespace.
        """
        self.existingdeployment(self.create_namespace(), "tocluster.py")

    def test_volumes(self):
        """
        Volumes are accessible locally.
        """
        self.existingdeployment(None, "volumes.py")

    def test_unsupportedtools(self):
        """
        Unsupported command line tools like ping fail nicely.
        """
        p = Popen(
            args=[
                "telepresence",
                "--method",
                TELEPRESENCE_METHOD,
                "--new-deployment",
                random_name(),
                "--logfile",
                "-",
                "--run",
                "python3",
                "unsupportedcli.py",
            ],
            cwd=str(DIRECTORY),
        )
        exit_code = p.wait()
        assert exit_code == 113

    def test_swapdeployment(self):
        """
        --swap-deployment swaps out Telepresence pod and then swaps it back on
        exit, when original pod was created with `kubectl run` or `oc run`.
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
        # We swapped back:
        assert deployment["spec"]["replicas"] == replicas

        # Ensure pods swap back too:
        start = time.time()
        while time.time() - start < 60:
            pods = json.loads(
                str(
                    check_output([
                        KUBECTL, "get", "pod", "--selector=" + selector, "-o",
                        "json", "--export"
                    ]), "utf-8"
                )
            )["items"]
            if [
                pod["spec"]["containers"][0]["image"]
                .startswith("openshift/hello-openshift") for pod in pods
            ] == [True] * len(pods):
                print("Found openshift!")
                return
            time.sleep(1)
        assert False, "Didn't switch back to openshift"

    def test_swapdeployment_explicit_container(self):
        """
        --swap-deployment <dep>:<container> swaps out the given container.
        """
        # Create a non-Telepresence Deployment with multiple containers:
        name = random_name()
        container_name = random_name()
        deployment = EXISTING_DEPLOYMENT.format(
            name=name,
            container_name=container_name,
            image="openshift/hello-openshift",
            namespace=current_namespace(),
            replicas=2
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
        self.addCleanup(
            check_output, [
                KUBECTL,
                "delete",
                DEPLOYMENT_TYPE,
                name,
            ]
        )

        p = Popen(
            args=[
                "telepresence", "--swap-deployment",
                "{}:{}".format(name,
                               container_name), "--logfile", "-", "--method",
                TELEPRESENCE_METHOD, "--run", "python3", "volumes.py"
            ],
            cwd=str(DIRECTORY),
        )
        exit_code = p.wait()
        assert 113 == exit_code


@skipUnless(TELEPRESENCE_METHOD == "container", "requires Docker")
class DockerEndToEndTests(TestCase):
    """End-to-end tests on Docker."""

    def get_containers(self):
        return set(check_output(["sudo", "docker", "ps", "-q"]).split())

    def setUp(self):
        self.containers = self.get_containers()

    def tearDown(self):
        # Ensure no container leaks
        time.sleep(1)
        assert self.containers == self.get_containers()

    def test_tocluster(self):
        """
        Tests of communication to the cluster from a Docker container.
        """
        webserver_name = run_webserver()
        result = run([
            "telepresence",
            "--logfile",
            "-",
            "--method",
            "container",
            "--new-deployment",
            random_name(),
            "--docker-run",
            "-v",
            "{}:/host".format(DIRECTORY),
            "python:3-alpine",
            "python3",
            "/host/tocluster.py",
            webserver_name,
            current_namespace(),
        ])
        assert result.returncode == 113

    def test_fromcluster(self):
        """
        The cluster can talk to a process running in a Docker container.
        """
        service_name = random_name()
        port = 12350
        p = Popen(
            args=[
                "telepresence", "--new-deployment", service_name, "--expose",
                str(port), "--logfile", "-", "--method", "container",
                "--docker-run", "-v",
                "{}:/host".format(DIRECTORY), "--workdir", "/host",
                "python:3-alpine", "python3",
                "-m", "http.server", str(port)
            ],
        )

        assert_fromcluster(current_namespace(), service_name, port)
        p.terminate()
        p.wait()

    def test_volumes(self):
        """
        Test availability of volumes in the container.
        """
        result = run([
            "telepresence",
            "--logfile",
            "-",
            "--method",
            "container",
            "--new-deployment",
            random_name(),
            "--docker-run",
            "-v",
            "{}:/host".format(DIRECTORY),
            "python:3-alpine",
            "python3",
            "/host/volumes_simpler.py",
        ])
        assert result.returncode == 113
