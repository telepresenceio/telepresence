"""
End-to-end tests which launch Telepresence and verify user-facing
behaviors.
"""

from json import (
    loads,
)
from urllib.request import (
    urlopen,
)
from itertools import (
    product,
)
from ipaddress import (
    IPv4Address,
)
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



@pytest.fixture(scope="session")
def origin_ip():
    return IPv4Address(httpbin_ip())



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


@with_probe
def test_unsupported_tools(probe):
    """
    In the Telepresence execution context, unsupported command line tools like
    ping fail nicely.
    """
    probe_result = probe.result()
    for (command, (success, result)) in probe_result.result["probe-commands"]:
        if probe.method.command_has_graceful_failure(command):
            assert not success and result == 55, (
                "{} expected to fail".format(command)
            )


@with_probe
def test_volumes(probe):
    """
    The Telepresence execution context exposes volumes.
    """
    probe_result = probe.result()
    path_contents = dict(probe_result.result["probe-paths"])

    if probe.operation.inherits_deployment_environment():
        assert 'hello="monkeys"' in path_contents["podinfo/labels"]
    else:
        assert path_contents["podinfo/labels"] is None

    assert path_contents[
        "var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
    ].startswith(
        "-----BEGIN CERT"
    )


@with_probe
def test_network_routing_to_cluster(probe):
    """
    The Telepresence execution context provides network routing for traffic
    originated in that context destined for addresses served by resources on
    the Kubernetes cluster.
    """
    probe_result = probe.result()

    probe_environ = probe_result.result["environ"]
    service_env = probe_result.webserver_name.upper().replace("-", "_")
    host = probe_environ[service_env + "_SERVICE_HOST"]
    port = probe_environ[service_env + "_SERVICE_PORT"]

    # Check the partial service domain name with hard-coded port.
    svc_url = "http://{}:8080/".format(probe_result.webserver_name)
    (success, response) = probe_url(probe_result, svc_url)
    assert success and "Hello" in response

    # Check the name as defined by the environment service variables.
    svc_url = "http://{}:{}/".format(host, port)
    (success, response) = probe_url(probe_result, svc_url)
    assert success and "Hello" in response

    if probe.method.name == "inject-tcp":
        # Check the full service domain name.
        svc_url = "http://{}.{}.svc.cluster.local:{}/".format(
            probe_result.webserver_name,
            probe_result.deployment_ident.namespace,
            port,
        )
        (success, response) = probe_url(probe_result, svc_url)
        assert success and "Hello" in response



def probe_url(probe_result, url):
    probe_result.write("probe-url {}".format(url))
    return loads(probe_result.read())[0][1]



@with_probe
def test_network_routing_also_proxy_hostname(probe, origin_ip):
    """
    The ``--also-proxy`` option accepts a hostname and arranges to have
    traffic for that host proxied via via the cluster.  The hostname must
    be resolveable on the cluster and the address reached from it.
    """
    probe_result = probe.result()

    (success, request_ip) = probe_also_proxy(
        probe_result,
        probe.ALSO_PROXY_HOSTNAME.host,
    )
    assert success and origin_ip != request_ip


@with_probe
def test_network_routing_also_proxy_ip_literal(probe, origin_ip):
    """
    The ``--also-proxy`` option accepts a single IP address given by a literal
    and arranges to have traffic for addresses in that range proxied via the
    cluster.
    """
    probe_result = probe.result()

    (success, request_ip) = probe_also_proxy(
        probe_result,
        probe.ALSO_PROXY_IP.host,
    )
    assert success and origin_ip != request_ip


@with_probe
def test_network_routing_also_proxy_ip_cidr(probe, origin_ip):
    """
    The ``--also-proxy`` option accepts an IP range given by a CIDR-notation
    string and arranges to have traffic for addresses in that range
    proxied via the cluster.
    """
    probe_result = probe.result()

    (success, request_ip) = probe_also_proxy(
        probe_result,
        probe.ALSO_PROXY_CIDR.host,
    )
    assert success and origin_ip != request_ip


def httpbin_ip():
    result = str(urlopen("http://httpbin.org/ip", timeout=30).read(), "utf-8")
    origin = loads(result)["origin"]
    return origin


def probe_also_proxy(probe_result, hostname):
    probe_result.write("probe-also-proxy {}".format(hostname))
    success, request_ip = loads(probe_result.read())
    return success, IPv4Address(request_ip)
