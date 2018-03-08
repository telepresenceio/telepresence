"""Utilities."""

import atexit
from pathlib import Path
import time
import os
from json import dumps
from base64 import b64encode
from subprocess import (
    check_output,
    STDOUT,
    check_call,
    CalledProcessError,
)


DIRECTORY = Path(__file__).absolute().parent
REVISION = str(check_output(["git", "rev-parse", "--short", "HEAD"]),
               "utf-8").strip()
START_TIME = time.time()
OPENSHIFT = os.environ.get('TELEPRESENCE_OPENSHIFT')

if OPENSHIFT:
    KUBECTL = "oc"
else:
    KUBECTL = "kubectl"


def random_name(suffix=""):
    """Return a new name each time."""
    if suffix and not suffix.startswith("-"):
        suffix = "-" + suffix
    return "testing-{}-{}-{}{}".format(
        REVISION, os.getpid(), time.time() - START_TIME, suffix
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
                'kubectl', 'run', '--attach', random_name("q"), "--quiet",
                '--rm', '--image=alpine', '--restart', 'Never', "--namespace",
                namespace, '--command', '--', 'wget', "-q", "-O-",
                "-T", "3", url,
            ])
        except CalledProcessError as e:
            if process_to_poll is not None and process_to_poll.poll() is not None:
                raise RuntimeError("Process exited prematurely: {}".format(process_to_poll.returncode))
            print("http request failed, sleeping before retry ({}; {})".format(e, e.output))
            time.sleep(1)
            continue
    raise RuntimeError("failed to connect to HTTP server " + url)


def query_from_cluster(url, namespace, tries=10, retries_on_empty=0):
    """
    Run an HTTP request from the cluster with timeout and retries
    """
    # Separate debug output from the HTTP server response.
    delimiter = b64encode(
        b"totally random stuff that won't appear anywhere else"
    ).decode("utf-8")
    shell_command = (
        """
        set -e
        for value in $(seq {tries}); do
            sleep 1
            wget --server-response --output-document=output -T3 {url} 2>&1 && break
        done
        echo {delimiter}
        [ -e output ] && cat output
        """).format(tries=tries, url=url, delimiter=delimiter)
    print("Querying {url} (tries={tries} empty-retries={empty})".format(
        url=url, tries=tries, empty=retries_on_empty,
    ))
    for _ in range(retries_on_empty + 1):
        res = check_output([
            "kubectl", "--namespace={}".format(namespace),
            "run", random_name("query"),
            "--attach", "--quiet", "--rm",
            "--image=alpine", "--restart=Never",
            "--command", "--", "sh", "-c", shell_command,
        ]).decode("utf-8")
        print("query output:")
        print(_indent(res))
        if res:
            debug, res = res.split(delimiter + "\n")
            return res
        print("... empty response")
    return res


def _indent(text):
    return "\t" + text.replace("\n", "\t\n")


def run_webserver(namespace=None):
    """Run webserver in Kubernetes; return Service name."""
    webserver_name = random_name("web")
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


def create_namespace(namespace_name, name):
    namespace = dumps({
        "kind": "Namespace",
        "apiVersion": "v1",
        "metadata": {
            "name": namespace_name,
            "labels": {
                "telepresence-test": name,
            },
        },
    })
    check_output([KUBECTL, "create", "-f", "-"], input=namespace.encode("utf-8"))


def cleanup_namespace(namespace_name):
    check_call([KUBECTL, "delete", "namespace", namespace_name])
