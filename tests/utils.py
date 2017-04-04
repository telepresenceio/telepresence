"""Utilities."""

import atexit
from pathlib import Path
import time
from subprocess import check_output, STDOUT, check_call, CalledProcessError

DIRECTORY = Path(__file__).absolute().parent
REVISION = str(check_output(["git", "rev-parse", "--short", "HEAD"]),
               "utf-8").strip()


def random_name():
    """Return a new name each time."""
    return "testing-{}-{}".format(REVISION, time.time()).replace(".", "-")


def telepresence_version():
    """Return the version of Telepresence we're testing."""
    return str(
        check_output(["telepresence", "--version"], stderr=STDOUT), "utf-8"
    ).strip()


def run_nginx(namespace):
    """Run nginx in Kuberentes; return Service name."""
    nginx_name = random_name()

    def cleanup():
        check_call([
            "kubectl", "delete", "--ignore-not-found", "--namespace",
            namespace, "service,deployment", nginx_name
        ])

    cleanup()
    atexit.register(cleanup)

    check_output([
        "kubectl",
        "run",
        "--namespace",
        namespace,
        "--generator",
        "deployment/v1beta1",
        nginx_name,
        "--image=nginx:alpine",
        "--port=80",
        "--expose",
    ])
    for i in range(120):
        try:
            available = check_output([
                "kubectl", "get", "deployment", nginx_name, "--namespace",
                namespace, "-o", 'jsonpath={.status.availableReplicas}'
            ])
        except CalledProcessError:
            available = None
        print("nginx available replicas: {}".format(available))
        if available == b"1":
            return nginx_name
        else:
            time.sleep(1)
    raise RuntimeError("nginx never started")
