"""Utilities."""

import atexit
from pathlib import Path
import socket
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

EXISTING_DEPLOYMENT = """\
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {name}
  namespace: {namespace}
data:
  EXAMPLE_ENVFROM: foobar
  EX_MULTI_LINE: |
    first line (no newline before, newline after)
    second line (newline before and after)
---
%s
metadata:
  name: {name}
  namespace: {namespace}
spec:
  replicas: {replicas}
  template:
    metadata:
      labels:
        name: {name}
        hello: monkeys  # <-- used by volumes test
    spec:
      containers:
      # Extra container at start to demonstrate we can handle multiple
      # containers
      - name: getintheway
        image: openshift/hello-openshift
        resources:
          limits:
            cpu: "100m"
            memory: "150Mi"
      - name: {container_name}
        image: {image}
        envFrom:
        - configMapRef:
            name: {name}
        env:
        - name: MYENV
          value: hello
        volumeMounts:
        - name: podinfo
          mountPath: /podinfo
        resources:
          requests:
            cpu: "100m"
            memory: "150Mi"
          limits:
            cpu: "100m"
            memory: "150Mi"
      volumes:
      - name: podinfo
        downwardAPI:
          items:
            - path: "labels"
              fieldRef:
                fieldPath: metadata.labels
"""

if OPENSHIFT:
    KUBECTL = "oc"
    EXISTING_DEPLOYMENT = EXISTING_DEPLOYMENT % ("""\
apiVersion: v1
kind: DeploymentConfig""",)
    DEPLOYMENT_TYPE = "deploymentconfig"
else:
    KUBECTL = "kubectl"
    EXISTING_DEPLOYMENT = EXISTING_DEPLOYMENT % ("""\
apiVersion: extensions/v1beta1
kind: Deployment""",)
    DEPLOYMENT_TYPE = "deployment"


def random_name(suffix=""):
    """Return a new name each time."""
    if suffix and not suffix.startswith("-"):
        suffix = "-" + suffix
    hostname = socket.gethostname()
    return "testing-{}-{}-{}-{}{}".format(
        REVISION, hostname, os.getpid(), time.time() - START_TIME, suffix
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
        # Make sure we'll stop with an error if anything we try to do stops
        # with an error.
        set -e

        # Try wget the number of times we were asked.
        for value in $(seq {tries}); do
            # Don't try too fast.
            sleep 1

            # server-response gets us the response headers which we'll dump
            # later to help debug any unexpected failures.
            #
            # output-document gets the content to a file which we can read out
            # later.  We want to do it later so that the caller has a chance
            # of parsing the overall script output.  If we let the page output
            # arrive now it gets mixed with other wget output and things get
            # confusing.
            #
            # -T3 sets a timeout for this particular request.
            #
            # If this request succeeds then we're done and we can break out of
            # the loop.
            wget --server-response --output-document=output -T3 {url} 2>&1 && break
        done

        # wget output is over.  Put this known string into the output here to
        # separate all that stuff from the response body which comes next.
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
