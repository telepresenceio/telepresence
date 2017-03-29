"""
Tests that accessing remote cluster from local container.

This module will be run inside a container. To indicate success it will print
"SUCCESS!".
"""

import os
import ssl
import sys
from traceback import print_exception
from urllib.request import urlopen
from urllib.error import HTTPError


def handle_error(type, value, traceback):
    print_exception(type, value, traceback, file=sys.stderr)
    raise SystemExit(3)


def check_kubernetes_api_url(url, how):
    # XXX Perhaps we can have a more robust test by starting our own service.
    print("Retrieving URL created with {}: {}".format(url, how))
    context = ssl._create_unverified_context()
    try:
        urlopen(url, timeout=5, context=context)
    except HTTPError as e:
        # Unauthorized (401) is default response code for Kubernetes API
        # server.
        if e.code == 401:
            return
        raise


def check_urls():
    host = os.environ["KUBERNETES_SERVICE_HOST"]
    port = os.environ["KUBERNETES_SERVICE_PORT"]
    # Check environment variable based service lookup:
    check_kubernetes_api_url(
        "https://{}:{}/".format(
            host,
            port,
        ), "env variables"
    )
    # Check hostname lookup, both partial and full:
    check_kubernetes_api_url(
        "https://kubernetes:{}/".format(port), "service name"
    )
    check_kubernetes_api_url(
        "https://kubernetes.default.svc.cluster.local:{}/".format(port),
        "full service name",
    )
    check_kubernetes_api_url("https://kubernetes:443/", "hardcoded port")
    return host, port


def check_env(host, port):
    # Check that other environment variable variants were created correctly:
    assert os.environ["KUBERNETES_PORT"] == "tcp://{}:{}".format(host, port)
    prefix = "KUBERNETES_PORT_{}_TCP".format(port)
    assert os.environ[prefix] == os.environ["KUBERNETES_PORT"]
    assert os.environ[prefix + "_PROTO"] == "tcp"
    assert os.environ[prefix + "_PORT"] == port
    assert os.environ[prefix + "_ADDR"] == host
    # This env variable is set in the remote pod itself, but we don't expect it
    # to be copied to local setup since it's not set explicitly on the
    # Deployment:
    assert "TELEPRESENCE_PROXY" not in os.environ


def check_custom_env():
    # Check custom environment variables are copied:
    for env in sys.argv[1:]:
        key, value = env.split("=", 1)
        assert os.environ[key] == value


def main():
    # make sure exceptions cause exit:
    sys.excepthook = handle_error

    # run tests
    host, port = check_urls()
    check_env(host, port)
    check_custom_env()

    # Exit successfully:
    sys.exit(0)


if __name__ == '__main__':
    main()
