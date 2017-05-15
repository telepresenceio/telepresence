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
    # XXX
    kubectl = ["oc"]
    if namespace is not None:
        kubectl.extend(["--namespace", namespace])

    def cleanup():
        check_call(
            kubectl + [
                "delete", "--ignore-not-found", "all",
                "--selector=telepresence=" + nginx_name
            ]
        )

    cleanup()
    atexit.register(cleanup)

    check_output(
        kubectl + [
            "run",
            "--restart=Never",
            nginx_name,
            "--labels=telepresence=" + nginx_name,
            "--image=openshift/hello-openshift",
            # XXX
            #"--limits=memory=128M",
            #"--requests=memory=64M",
            "--port=8080",
            "--expose",
        ]
    )
    for i in range(120):
        try:
            available = check_output(
                kubectl + [
                    # XXX
                    "get", "pods", nginx_name, "-o",
                    'jsonpath={.status.phase}'
                ]
            )
        except CalledProcessError:
            available = None
        print("webserver phase: {}".format(available))
        if available == b"Running":
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
    ) or "default"
