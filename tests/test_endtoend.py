"""
End-to-end Telepresence tests for running directly in the operating system.
"""

from sys import executable
from json import loads, dumps
from unittest import (
    TestCase,
)
from subprocess import (
    CalledProcessError,
    PIPE, STDOUT, Popen, check_output, check_call,
)
from pathlib import Path
from shutil import which

from .utils import (
    KUBECTL,
    random_name,
)

from .rwlock import RWLock


network = RWLock()


class ResourceIdent(object):
    def __init__(self, namespace, name):
        self.namespace = namespace
        self.name = name


def _telepresence(telepresence_args):
    """
    Run a probe in a Telepresence execution context.
    """
    args = [
        executable, which("telepresence"),
        "--logfile", "-",
    ] + telepresence_args
    return check_output(
        args=args,
        stdin=PIPE,
        stderr=STDOUT,
    )



class _EndToEndTestsMixin(object):
    """
    A mixin for ``TestCase`` defining various end-to-end tests for
    Telepresence.
    """
    DESIRED_ENVIRONMENT = {
        "MYENV": "hello",
        "EXAMPLE_ENVFROM": "foobar",

        # XXXX
        # Container method doesn't support multi-line environment variables.
        # Therefore disable this for all methods or the container tests all
        # fail...
        # XXXX
        # "EX_MULTI_LINE": (
        #     "first line (no newline before, newline after)\n"
        #     "second line (newline before and after)\n"
        # ),
    }

    def __init__(self, method, operation):
        self._method = method
        self._operation = operation


    def setUp(self):
        deployment_ident = self._operation.prepare_deployment(self.DESIRED_ENVIRONMENT)
        self.addCleanup(self._cleanup_deployment, deployment_ident)
        operation_args = self._operation.telepresence_args(deployment_ident)

        probe_endtoend = (Path(__file__).parent / "probe_endtoend.py").as_posix()
        method_args = self._method.telepresence_args(probe_endtoend)

        try:
            output = _telepresence(operation_args + method_args)
        except CalledProcessError as e:
            print(e.output.decode("utf-8"))
            self.fail("Sorry")
        else:
            self.probe_result = loads(output)


    def test_environment(self):
        probe_environment = self.probe_result["environ"]
        for k in self.DESIRED_ENVIRONMENT:
            self.assertEqual(self.DESIRED_ENVIRONMENT[k], probe_environment[k])


    def _cleanup_deployment(self, ident):
        check_call([
            KUBECTL, "delete",
            "--namespace", ident.namespace,
            "deployment", ident.name,
        ])



class _VPNTCPMethod(object):
    def lock(self):
        network.lock_write()


    def release(self):
        network.unlock_write()


    def telepresence_args(self, probe):
        return [
            "--method", "vpn-tcp",
            "--run", executable, probe,
        ]



class _InjectTCPMethod(object):
    def lock(self):
        network.lock_read()


    def release(self):
        network.unlock_read()


    def telepresence_args(self, probe):
        return [
            "--method", "inject-tcp",
            "--run", executable, probe,
        ]



class _ContainerMethod(object):
    def lock(self):
        network.lock_read()


    def release(self):
        network.unlock_read()


    def telepresence_args(self, probe):
        return [
            "--method", "container",
            "--docker-run",
            "--volume", "{}:/probe.py".format(probe),
            "python:3-alpine",
            "python", "/probe.py",
        ]


def create_deployment(environ):
    def env_arguments(environ):
        return list(
            "--env={}={}".format(k, v)
            for (k, v)
            in environ.items()
        )
    name = random_name()
    namespace_name = random_name()
    namespace = dumps({
        "kind": "Namespace",
        "apiVersion": "v1",
        "metadata": {
            "name": namespace_name,
            "labels": {
                "telepresence-test": "name",
            },
        },
    })
    deployment = dumps({
        "kind": "Deployment",
        "apiVersion": "extensions/v1beta1",
        "metadata": {
            "name": name,
            "namespace": namespace_name,
        },
        "spec": {
            "replicas": 2,
            "template": {
                "metadata": {
                    "labels": {
                        "name": name,
                        "telepresence-test": name,
                    },
                },
                "spec": {
                    "containers": [{
                        "name": "hello",
                        "image": image,
                        "env": list(
                            {"name": k, "value": v}
                            for (k, v)
                            in environ.items()
                        ),
                    }],
                },
            },
        },
    })
    check_output([KUBECTL, "create", "-f", "-"], input=namespace.encode("utf-8"))
    check_output([KUBECTL, "create", "-f", "-"], input=deployment.encode("utf-8"))
    return ResourceIdent(namespace_name, name)



class _ExistingDeploymentOperation(object):
    def __init__(self, swap):
        self.swap = swap


    def prepare_deployment(self, environ):
        if self.swap:
            return create_deployment("openshift/hello-openshift", environ)
        return create_deployment("datawire/telepresence-k8s", environ)


    def telepresence_args(self, deployment_ident):
        if self.swap:
            option = "--swap-deployment"
        else:
            option = "--deployment"
        return [
            "--namespace", deployment_ident.namespace,
            option, deployment_ident.name,
        ]



def telepresence_tests(method, operation):
    class Tests(_EndToEndTestsMixin, TestCase):
        def __init__(self, *args, **kwargs):
            _EndToEndTestsMixin.__init__(self, method, operation)
            TestCase.__init__(self, *args, **kwargs)
    return Tests



class SwapEndToEndVPNTCPTests(telepresence_tests(
        _VPNTCPMethod(),
        _ExistingDeploymentOperation(True),
)):
    """
    Tests for the *vpn-tcp* method using a swapped Deployment.
    """



class SwapEndToEndInjectTCPTests(telepresence_tests(
        _InjectTCPMethod(),
        _ExistingDeploymentOperation(True),
)):
    """
    Tests for the *inject-tcp* method using a swapped Deployment.
    """



class SwapEndToEndContainerTests(telepresence_tests(
        _ContainerMethod(),
        _ExistingDeploymentOperation(True),
)):
    """
    Tests for the *container* method using a swapped Deployment.
    """


class ExistingEndToEndVPNTCPTests(telepresence_tests(
        _VPNTCPMethod(),
        _ExistingDeploymentOperation(False),
)):
    """
    Tests for the *vpn-tcp* method using an existing Deployment.
    """



class ExistingEndToEndInjectTCPTests(telepresence_tests(
        _InjectTCPMethod(),
        _ExistingDeploymentOperation(False),
)):
    """
    Tests for the *inject-tcp* method using an existing Deployment.
    """



class ExistingEndToEndContainerTests(telepresence_tests(
        _ContainerMethod(),
        _ExistingDeploymentOperation(False),
)):
    """
    Tests for the *container* method using an existing Deployment.
    """
