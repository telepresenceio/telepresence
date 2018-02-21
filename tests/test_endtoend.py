"""
End-to-end tests which launch Telepresence and verify user-facing
behaviors.
"""

from itertools import product

import pytest

from .parameterize_utils import (
    METHODS,
    OPERATIONS,
    Probe,
)

# Mark this as the `probe` fixture and declare that instances of it may be
# shared by any tests within the same module.
@pytest.fixture(scope="module")
def probe(request):
    method, operation = request.param
    reason = method.unsupported()
    if reason is None:
        probe = Probe(request, method, operation)
        yield probe
        probe.cleanup()
    else:
        pytest.skip(reason)


with_probe = pytest.mark.parametrize(
    # Parameterize the probe parameter to decorated methods
    "probe",

    # The parameters are the elements of the cartesian product of methods,
    # operations.
    list(product(METHODS, OPERATIONS)),

    # Use the `name` of methods and operations to generate readable
    # parameterized test names.
    ids=lambda param: "{},{}".format(param[0].name, param[1].name),

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
    if probe.operation.inherits_deployment_environment():
        # If the operation is expected to inherit an environment from an
        # existing Deployment, make sure that it did.
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

    if probe.method.inherits_client_environment():
        # Likewise, make an assertion about client environment being inherited
        # if this method is supposed to do that.
        assert (
            probe.CLIENT_ENV_VAR in probe_environment
        ), (
            "Telepresence client environment missing from Telepresence execution context."
        )
    else:
        assert (
            probe.CLIENT_ENV_VAR not in probe_environment
        ), (
            "Telepresence client environment leaked into Telepresence execution context."
        )


@with_probe
def test_environment_for_services(probe):
    """
    The Telepresence execution context supplies environment variables with
    values locating services configured on the cluster.
    """
    probe_result = probe.result()
    probe_environment = probe_result.result["environ"]
    webserver_name = probe_result.webserver_name

    service_env = webserver_name.upper().replace("-", "_")
    host = probe_environment[service_env + "_SERVICE_HOST"]
    port = probe_environment[service_env + "_SERVICE_PORT"]

    prefix = service_env + "_PORT_{}_TCP".format(port)
    desired_environment = {
        service_env + "_PORT": "tcp://{}:{}".format(host, port),
        prefix + "_PROTO": "tcp",
        prefix + "_PORT": port,
        prefix + "_ADDR": host,
    }

    assert (
        desired_environment ==
        {k: probe_environment.get(k, None) for k in desired_environment}
    ), (
        "Probe environment missing some expected items:\n"
        "Desired: {}\n"
        "Probed: {}\n".format(desired_environment, probe_environment),
    )
    assert (
        probe_environment[prefix] ==
        probe_environment[service_env + "_PORT"]
    )


@with_probe
def test_loopback_network_access(probe):
    """
    The Telepresence execution environment allows network access to the host
    at the loopback address.
    """
    if probe.method.loopback_is_host():
        probe_result = probe.result()
        (success, response) = next(
            result
            for url, result
            in probe_result.result["probe-urls"]
            if url == probe.loopback_url
        )

        # We're loading _this_ file via curl, so it should have the string
        # "cuttlefish" which is in this comment and unlikely to appear by
        # accident.
        assert success and u"cuttlefish" in response
