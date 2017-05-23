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
OPENSHIFT = os.environ.get('TELEPRESENCE_OPENSHIFT')

if OPENSHIFT:
    KUBECTL = "oc"
else:
    KUBECTL = "kubectl"


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


def run_webserver(namespace=None):
    """Run webserver in Kuberentes; return Service name."""
    webserver_name = random_name()
    kubectl = [KUBECTL]
    if namespace is not None:
        kubectl.extend(["--namespace", namespace])

    def cleanup():
        check_call(
            kubectl + [
                "delete", "--ignore-not-found", "all",
                "--selector=telepresence=" + webserver_name
            ]
        )

    cleanup()
    atexit.register(cleanup)

    check_output(
        kubectl + [
            "run",
            "--restart=Never",
            webserver_name,
            "--labels=telepresence=" + webserver_name,
            "--image=openshift/hello-openshift",
            "--limits=memory=256Mi",
            "--requests=memory=150Mi",
            "--port=8080",
            "--expose",
        ]
    )
    for i in range(120):
        try:
            available = check_output(
                kubectl + [
                    "get", "pods", webserver_name, "-o",
                    'jsonpath={.status.phase}'
                ]
            )
        except CalledProcessError:
            available = None
        print("webserver phase: {}".format(available))
        if available == b"Running":
            return webserver_name
        else:
            time.sleep(1)
    raise RuntimeError("webserver never started")


def current_namespace():
    """Return the current Kubernetes namespace."""
    return str(
        check_output([
            KUBECTL, "config", "view", "--minify=true",
            "-o=jsonpath={.contexts[0].context.namespace}"
        ]).strip(), "ascii"
    ) or "default"
