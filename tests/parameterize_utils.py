import os
from sys import executable
from json import (
    JSONDecodeError,
    loads, dumps,
)
from shutil import which
from subprocess import (
    CalledProcessError,
    PIPE, STDOUT, check_output, check_call,
)

from pathlib import Path

from .utils import (
    KUBECTL,
    random_name,
    run_webserver,
    create_namespace,
    telepresence_version,
)

from .rwlock import RWLock


# Some Telepresence configurations take over the machine's whole network
# stack.  We must avoid concurrency when running those tests.  The network
# lock gives us a way to do that.  Tests with an isolated network impact
# (inject-tcp, container) will do a read-acquire on this lock.  Tests with a
# global network impact (vpn-tcp) will do a write-acquire on this lock.
network = RWLock()

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")


class _ContainerMethod(object):
    name = "container"

    def lock(self):
        network.lock_read()


    def unlock(self):
        network.unlock_read()


    def telepresence_args(self, probe):
        return [
            "--method", "container",
            "--docker-run",
            "--volume", "{}:/probe.py".format(probe),
            "python:3-alpine",
            "python", "/probe.py",
        ]


class _InjectTCPMethod(object):
    name = "inject-tcp"

    def lock(self):
        network.lock_read()


    def unlock(self):
        network.unlock_read()


    def telepresence_args(self, probe):
        return [
            "--method", "inject-tcp",
            "--run", executable, probe,
        ]



class _VPNTCPMethod(object):
    name = "vpn-tcp"

    def lock(self):
        network.lock_write()


    def unlock(self):
        network.unlock_write()


    def telepresence_args(self, probe):
        return [
            "--method", "vpn-tcp",
            "--run", executable, probe,
        ]


class _ExistingDeploymentOperation(object):
    def __init__(self, swap):
        self.swap = swap
        if swap:
            self.name = "swap"
        else:
            self.name = "existing"


    def prepare_deployment(self, deployment_ident, environ):
        if self.swap:
            image = "openshift/hello-openshift"
        else:
            image = "{}/telepresence-k8s:{}".format(
                REGISTRY,
                telepresence_version(),
            )
        create_deployment(deployment_ident, image, environ)


    def telepresence_args(self, deployment_ident):
        if self.swap:
            option = "--swap-deployment"
        else:
            option = "--deployment"
        return [
            "--namespace", deployment_ident.namespace,
            option, deployment_ident.name,
        ]



class _NewDeploymentOperation(object):
    name = "new"

    def prepare_deployment(self, deployment_ident, environ):
        pass

    def telepresence_args(self, deployment_ident):
        return [
            "--namespace", deployment_ident.namespace,
            "--new-deployment", deployment_ident.name,
        ]



def create_deployment(deployment_ident, image, environ):
    def env_arguments(environ):
        return list(
            "--env={}={}".format(k, v)
            for (k, v)
            in environ.items()
        )
    deployment = dumps({
        "kind": "Deployment",
        "apiVersion": "extensions/v1beta1",
        "metadata": {
            "name": deployment_ident.name,
            "namespace": deployment_ident.namespace,
        },
        "spec": {
            "replicas": 2,
            "template": {
                "metadata": {
                    "labels": {
                        "name": deployment_ident.name,
                        "telepresence-test": deployment_ident.name,
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
    check_output([KUBECTL, "create", "-f", "-"], input=deployment.encode("utf-8"))


METHODS = [
    _ContainerMethod(),
    _InjectTCPMethod(),
    _VPNTCPMethod(),
]
OPERATIONS = [
    _ExistingDeploymentOperation(False),
    _ExistingDeploymentOperation(True),
    _NewDeploymentOperation(),
]

class ResourceIdent(object):
    """
    Identify a Kubernetes resource.
    """
    def __init__(self, namespace, name):
        self.namespace = namespace
        self.name = name


def _cleanup_deployment(ident):
    check_call([
        KUBECTL, "delete",
        "--namespace", ident.namespace,
        "--ignore-not-found",
        "deployment", ident.name,
    ])



def _telepresence(telepresence_args):
    """
    Run a probe in a Telepresence execution context.
    """
    args = [
        executable, which("telepresence"),
        "--logfile", "-",
    ] + telepresence_args
    print("Running {}".format(args))
    return check_output(
        args=args,
        stdin=PIPE,
        stderr=STDOUT,
    )



def run_telepresence_probe(request, method, operation, desired_environment):
    """
    :param request: The pytest mumble mumble whatever.

    :param method: The definition of a Telepresence method to use for this
        run.

    :param operation: The definition of a Telepresence operation to use
        for this run.

    :param dict desired_environment: Key/value pairs to set in the probe's
        environment.
    """
    probe_endtoend = (Path(__file__).parent / "probe_endtoend.py").as_posix()

    # Create a web server service.  We'll observe side-effects related to
    # this, such as things set in our environment, and also interact with
    # it directly to demonstrate behaviors related to networking.  It's
    # important that we create this before the ``prepare_deployment`` step
    # below because the environment supplied by Kubernetes to a
    # Deployment's containers depends on the state of the cluster at the
    # time of pod creation.
    deployment_ident = ResourceIdent(
        namespace=random_name(),
        name=random_name(),
    )
    create_namespace(deployment_ident.namespace, deployment_ident.name)
    webserver_name = run_webserver(deployment_ident.namespace)

    operation.prepare_deployment(deployment_ident, desired_environment)
    print("Prepared deployment {}/{}".format(deployment_ident.namespace, deployment_ident.name))
    request.addfinalizer(lambda: _cleanup_deployment(deployment_ident))

    operation_args = operation.telepresence_args(deployment_ident)
    method_args = method.telepresence_args(probe_endtoend)
    args = operation_args + method_args
    try:
        try:
            method.lock()
            output = _telepresence(args)
        finally:
            method.unlock()
    except CalledProcessError as e:
        assert False, "Failure running {}: {}\n{}".format(
            ["telepresence"] + args, str(e), e.output.decode("utf-8"),
        )
    else:
        # Scrape the payload out of the overall noise.
        output = output.split(b"{probe delimiter}")[1]
        try:
            probe_result = loads(output)
        except JSONDecodeError:
            assert False, "Could not decode JSON probe result from {}:\n{}".format(
                ["telepresence"] + args, output.decode("utf-8"),
            )
        return ProbeResult(webserver_name, probe_result)



class ProbeResult(object):
    def __init__(self, webserver_name, result):
        self.webserver_name = webserver_name
        self.result = result



class Probe(object):
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
    _result = None

    def __init__(self, request, method, operation):
        self._request = request
        self._method = method
        self._operation = operation


    def __str__(self):
        return "Probe[{}, {}]".format(
            self._method.name,
            self._operation.name,
        )


    def result(self):
        if self._result is None:
            print("Launching {}".format(self))
            self._result = run_telepresence_probe(
                self._request,
                self._method,
                self._operation,
                self.DESIRED_ENVIRONMENT,
            )
        return self._result


    def cleanup(self):
        print("Cleaning up {}".format(self))
