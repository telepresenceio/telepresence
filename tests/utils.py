"""Utilities."""

import atexit
from pathlib import Path
import time
import os
from subprocess import check_output, STDOUT, check_call, CalledProcessError

DIRECTORY = Path(__file__).absolute().parent
REVISION = str(check_output(["git", "rev-parse", "--short", "HEAD"]),
               "utf-8").strip()
START_TIME = time.time()


def random_name():
    """Return a new name each time."""
    return "testing-{}-{}-{}".format(
        REVISION, time.time() - START_TIME, os.getpid()
    ).replace(".", "-")


def telepresence_version():
    """Return the version of Telepresence we're testing."""
    return str(
        check_output(["telepresence", "--version"], stderr=STDOUT), "utf-8"
    ).strip()


def run_nginx(namespace=None):
    """Run nginx in Kuberentes; return Service name."""
    nginx_name = random_name()
    kubectl = ["kubectl"]
    if namespace is not None:
        kubectl.extend(["--namespace", namespace])

    def cleanup():
        check_call(
            kubectl +
            ["delete", "--ignore-not-found", "service,deployment", nginx_name]
        )

    cleanup()
    atexit.register(cleanup)

    check_output(
        kubectl + [
            "run",
            "--generator",
            "deployment/v1beta1",
            nginx_name,
            "--image=nginx:alpine",
            "--limits=memory=128M",
            "--requests=memory=64M",
            "--port=80",
            "--expose",
        ]
    )
    for i in range(120):
        try:
            available = check_output(
                kubectl + [
                    "get", "deployment", nginx_name, "-o",
                    'jsonpath={.status.availableReplicas}'
                ]
            )
        except CalledProcessError:
            available = None
        print("nginx available replicas: {}".format(available))
        if available == b"1":
            return nginx_name
        else:
            time.sleep(1)
    raise RuntimeError("nginx never started")


def current_namespace():
    """Return the current Kubernetes namespace."""
    return str(
        check_output([
            "kubectl", "config", "view", "--minify=true",
            "-o=jsonpath={.contexts[0].context.namespace}"
        ]).strip(), "ascii"
    )
