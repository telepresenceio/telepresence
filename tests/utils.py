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


def query_in_k8s(namespace, url, process_to_poll):
    """Try sending HTTP requests to URL in Kubernetes, returning result.

    On failure, retry. Will eventually timeout and raise exception.
    """
    for i in range(120):
        try:
            return check_output([
                'kubectl', 'run', '--attach', random_name(), "--quiet", '--rm',
                '--image=alpine', '--restart', 'Never', "--namespace",
                namespace, '--command', '--', 'wget', "-q", "-O-",
                "-T", "3", url,
            ])
        except CalledProcessError as e:
            if process_to_poll is not None and process_to_poll.poll() is not None:
                raise RuntimeError("Process exited prematurely: {}".format(process_to_poll.returncode))
            print("http request failed, sleeping before retry ({})".format(e))
            time.sleep(1)
            continue
    raise RuntimeError("failed to connect to HTTP server " + url)


def run_webserver(namespace=None):
    """Run webserver in Kuberentes; return Service name."""
    webserver_name = random_name()
    if namespace is None:
        namespace = current_namespace()
    kubectl = [KUBECTL, "--namespace", namespace]

    def cleanup():
        check_call(
            kubectl + [
                "delete", "--ignore-not-found", "all",
                "--selector=telepresence=" + webserver_name
            ]
        )

    cleanup()
    atexit.register(cleanup)

    print("Creating webserver {}/{}".format(namespace, webserver_name))
    check_output(
        kubectl + [
            "run",
            "--restart=Never",
            webserver_name,
            "--labels=telepresence=" + webserver_name,
            "--image=openshift/hello-openshift",
            "--limits=cpu=100m,memory=256Mi",
            "--requests=cpu=25m,memory=150Mi",
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
            # Wait for it to be running
            query_in_k8s(namespace, "http://{}:8080/".format(webserver_name), None)
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
