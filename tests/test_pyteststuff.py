
from json import (
    JSONDecodeError,
    loads, dumps,
)
from sys import executable
from shutil import which
from itertools import product
from subprocess import (
    CalledProcessError,
    PIPE, STDOUT, Popen, check_output, check_call,
)

from pathlib import Path

import pytest

from .utils import (
    KUBECTL,
    random_name,
    run_webserver,
    create_namespace,
)

from .test_endtoend import (
    _ContainerMethod,
    _InjectTCPMethod,
    _VPNTCPMethod,

    _ExistingDeploymentOperation,
    _NewDeploymentOperation,
)


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
    return check_output(
        args=args,
        stdin=PIPE,
        stderr=STDOUT,
    )



def run_telepresence_probe(request, method, operation, desired_environment):
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

    def result(self):
        if self._result is None:
            self._result = run_telepresence_probe(
                self._request,
                self._method,
                self._operation,
                self.DESIRED_ENVIRONMENT,
            )
        return self._result

    def cleanup(self):
        pass


@pytest.fixture(scope="module")
def probe(request):
    method, operation = request.param
    probe = Probe(request, method, operation)
    yield probe
    probe.cleanup()


with_probe = pytest.mark.parametrize(
    # Parameterize the probe parameter to decorated methods
    "probe",

    # The parameters are the elements of the cartesian product of methods,
    # operations.
    list(
        product(
            METHODS,
            OPERATIONS,
        ),
    ),

    # Use the `name` of methods and operations to generate readable
    # parameterized test names.
    ids=lambda param: "{} {}".format(param[0].name, param[1].name),

    # Pass the parameters through the probe fixture to get the object that's
    # really passed to the decorated function.
    indirect=True,
)



@with_probe
def test_environment_from_deployment(probe):
    """
    The Telepresence execution context supplies environment variables with
    values defined in the Kubernetes Deployment.
    """
    probe_environment = probe.result().result["environ"]
    assert (
        probe.DESIRED_ENVIRONMENT
        == {
            k: probe_environment.get(k, None)
            for k
            in probe.DESIRED_ENVIRONMENT
        }
    ), (
        "Probe environment missing some expected items:\n"
        "Desired: {}\n"
        "Probed: {}\n".format(
            probe.DESIRED_ENVIRONMENT,
            probe_environment,
        )
    )


@with_probe
def test_environment_from_buzz(probe):
    print(probe)
