"""
End-to-end tests which launch Telepresence and verify user-facing
behaviors.
"""

from pprint import (
    pformat,
)
from time import (
    time,
)
from json import (
    loads,
)
from urllib.request import (
    urlopen,
)
from ipaddress import (
    IPv4Address,
)
from subprocess import (
    check_output,
)

import pytest

from .conftest import (
    with_probe,
    after_probe,
)
from .utils import (
    KUBECTL,
    DEPLOYMENT_TYPE,
    query_from_cluster,
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


@with_probe
def test_network_routing_from_cluster(probe):
    """
    The Kubernetes cluster can route traffic to the Telepresence execution
    context.
    """
    if probe.operation.name == "new":
        pytest.xfail("Issue 494")
    http = probe.HTTP_SERVER_SAME_PORT
    query_result = query_http_server(probe.result(), http)
    assert query_result == http.value


@with_probe
def test_network_routing_from_cluster_local_port(probe):
    """
    The cluster can talk to a process running in a Docker container, with
    the local process listening on a different port.
    """
    if probe.operation.name == "new":
        pytest.xfail("Issue 494")
    http = probe.HTTP_SERVER_DIFFERENT_PORT
    query_result = query_http_server(probe.result(), http)
    assert query_result == http.value


@with_probe
def test_network_routing_from_cluster_low_port(probe):
    """
    Communicate from the cluster to Telepresence, with port<1024.
    """
    if probe.operation.name == "existing":
        pytest.xfail("Issue 496")
    http = probe.HTTP_SERVER_LOW_PORT
    query_result = query_http_server(probe.result(), http)
    assert query_result == http.value


@with_probe
def test_network_routing_from_cluster_auto_expose_same(probe):
    """
    --swap-deployment auto-exposes ports listed in the Deployment.

    Important that the test uses port actually used by original container,
    otherwise we will miss bugs where a Telepresence proxy container is added
    rather than being swapped.
    """
    if probe.operation.name != "swap":
        pytest.skip("Test only applies to --swap-deployment usage.")

    result = probe.result()
    http = probe.operation.http_server_auto_expose_same
    query_result = query_http_server(result, http)
    assert query_result == http.value


@with_probe
@pytest.mark.xfail
def test_network_routing_from_cluster_auto_expose_diff(probe):
    """
    Like ``test_network_routing_from_cluster_auto_expose_same`` but for the
    case where the exposed port and the container port are different.
    """
    if probe.operation.name != "swap":
        pytest.skip("Test only applies to --swap-deployment usage.")

    result = probe.result()
    http = probe.operation.http_server_auto_expose_diff
    query_result = query_http_server(result, http)
    assert query_result == http.value


def query_http_server(probe_result, http):
    """
    Request a resource from one of the HTTP servers begin run by the probe
    process.

    :param ProbeResult probe_result: A probe result we can use to help find
        the desired HTTP server.

    :param HTTPServer http: The particular HTTP server to which we want to
        issue a request.

    :return str: The response body.
    """
    ident = probe_result.deployment_ident
    url = "http://{}.{}:{}/random_value".format(
        ident.name,
        ident.namespace,
        http.remote_port,
    )
    return query_from_cluster(
        url,
        ident.namespace,
        tries=10,
        retries_on_empty=5,
    )


def httpbin_ip():
    result = str(urlopen("http://httpbin.org/ip", timeout=30).read(), "utf-8")
    origin = loads(result)["origin"]
    return origin


def probe_also_proxy(probe_result, hostname):
    probe_result.write("probe-also-proxy {}".format(hostname))
    success, request_ip = loads(probe_result.read())
    return success, IPv4Address(request_ip)


def _get_swap_result(probe):
    if probe.operation.name != "swap":
        pytest.skip("Test only applies to --swap-deployment usage.")

    # Telepresence won't try to swap anything back until it believes its job
    # is done.  So make sure its job is done before we make any assertions
    # about whether things were swapped back.
    probe.ensure_dead()
    return probe.result()


@with_probe
def test_resolve_names(probe):
    """
    Name resolution is performed in the context of the Kubernetes cluster.
    """
    result = probe.result()
    result.write("gethostbyname {}.{}".format(
        result.deployment_ident.name,
        result.deployment_ident.namespace,
    ))
    reply = loads(result.read())
    assert IPv4Address(reply)


@with_probe
def test_resolve_addresses(probe):
    """
    Reverse name resolution is performed in the context of the Kubernetes
    cluster.
    """
    result = probe.result()
    result.write("gethostbyaddr 4.2.2.1")
    reply = loads(result.read())
    assert "level3.net" in reply[0]


@after_probe
def test_swapdeployment_restores_container_image(probe):
    """
    After a Telepresence session with ``--swap-deployment`` exits, the image
    specified by the original deployment has been restored to the Kubernetes
    Deployment resource.
    """
    result = _get_swap_result(probe)
    deployment = get_deployment(result.deployment_ident)
    images = {
        container["image"]
        for container
        in deployment["spec"]["template"]["spec"]["containers"]
    }
    assert {probe.operation.image} == images


@after_probe
def test_swapdeployment_restores_container_command(probe):
    """
    After a Telepresence session with ``--swap-deployment`` exits, the image
    specified by the original deployment has been restored to the Kubernetes
    Deployment resource.
    """
    result = _get_swap_result(probe)
    deployment = get_deployment(result.deployment_ident)
    args = [
        container["args"]
        for container
        in deployment["spec"]["template"]["spec"]["containers"]
    ]
    assert [probe.operation.container_args] == args


@after_probe
def test_swapdeployment_restores_deployment_pods(probe):
    """
    After a Telepresence session with ``--swap-deployment`` exits, pods with
    the image specified by the original have been restored.
    """
    result = _get_swap_result(probe)
    start = time()
    while time() < start + 60:
        pods = get_pods(result.deployment_ident)["items"]
        image_and_phase = [
            (pod["spec"]["containers"][0]["image"], pod["status"]["phase"])
            for pod
            in pods
        ]
        if all(
            image.startswith(probe.operation.image)
            for (image, phase)
            in image_and_phase
        ):
            # Found the images we want, success.
            return

    # Ran out of time.
    selector = "telepresence-test={}".format(result.deployment_ident.name)
    assert False, \
        "Didn't switch back: \n\t{}\n{}".format(
            image_and_phase,
            pformat(kubectl(
                "get", "-o", "json", "all", "--selector", selector,
            )),
        )


@after_probe
def test_swapdeployment_restores_deployment_replicas(probe):
    """
    After a Telepresence session with ``--swap-deployment`` exits, the replica
    configuration specified by the original deployment has been restored to
    the Kubernetes Deployment resource.
    """
    result = _get_swap_result(probe)
    deployment = get_deployment(result.deployment_ident)
    assert probe.operation.replicas == deployment["spec"]["replicas"]



def kubectl(*argv):
    return loads(check_output((KUBECTL,) + argv).decode("utf-8"))



def get_deployment(ident):
    return kubectl(
        "get",
        "--namespace", ident.namespace,
        DEPLOYMENT_TYPE, ident.name,
        "-o", "json",
        "--export",
    )



def get_pods(ident):
    return kubectl(
        "get",
        "--namespace", ident.namespace,
        "pod",
        "--selector", "telepresence-test={}".format(ident.name),
        "-o", "json",
        "--export",
    )
