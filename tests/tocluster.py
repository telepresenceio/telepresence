"""
Tests that accessing remote cluster from local container.

This module will indicate success it will exit with code 113.
"""

import os
import sys
from traceback import print_exception
from urllib.request import urlopen


def handle_error(type, value, traceback):
    print_exception(type, value, traceback, file=sys.stderr)
    raise SystemExit(3)


def check_webserver_url(url, how):
    print("Retrieving URL created with {}: {}".format(url, how))
    result = str(urlopen(url, timeout=5).read(), "utf-8")
    print("Got {} from webserver.".format(repr(result)))
    assert "Hello" in result


def check_urls(webserver_service, namespace):
    service_env = webserver_service.upper().replace("-", "_")
    host = os.environ[service_env + "_SERVICE_HOST"]
    port = os.environ[service_env + "_SERVICE_PORT"]
    # Check environment variable based service lookup:
    check_webserver_url("http://{}:{}/".format(
        host,
        port,
    ), "env variables")
    # Check hostname lookup, both partial and full:
    check_webserver_url(
        "http://{}:{}/".format(webserver_service, port), "service name"
    )
    if os.environ["TELEPRESENCE_METHOD"] == "inject-tcp":
        check_webserver_url(
            "http://{}.{}.svc.cluster.local:{}/".format(
                webserver_service, namespace, port
            ),
            "full service name",
        )
    check_webserver_url(
        "http://{}:8080/".format(webserver_service), "hardcoded port"
    )
    return host, port


def check_env(webserver_service, host, port):
    # Check that other environment variable variants were created correctly:
    service_env = webserver_service.upper().replace("-", "_")
    assert os.environ[service_env +
                      "_PORT"] == "tcp://{}:{}".format(host, port)
    prefix = service_env + "_PORT_{}_TCP".format(port)
    assert os.environ[prefix] == os.environ[service_env + "_PORT"]
    assert os.environ[prefix + "_PROTO"] == "tcp"
    assert os.environ[prefix + "_PORT"] == port
    assert os.environ[prefix + "_ADDR"] == host
    # This env variable is set in the remote pod itself, but we don't expect it
    # to be copied to local setup since it's not set explicitly on the
    # Deployment:
    assert "TELEPRESENCE_PROXY" not in os.environ


def check_custom_env(envs):
    # Check custom environment variables are copied:
    for env in envs:
        key, value = env.split("=", 1)
        assert os.environ[key] == value


def main():
    # make sure exceptions cause exit:
    sys.excepthook = handle_error

    webserver_service = sys.argv[1]
    namespace = sys.argv[2]
    envs = sys.argv[3:]

    # run tests
    host, port = check_urls(webserver_service, namespace)
    check_env(webserver_service, host, port)
    check_custom_env(envs)

    # Exit with code indicating success:
    sys.exit(113)


if __name__ == '__main__':
    main()
