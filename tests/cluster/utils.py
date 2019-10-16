"""Utilities."""

import os
import socket
import time
from base64 import b64encode
from json import dumps
from pathlib import Path
from subprocess import CalledProcessError, check_call, check_output

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
    EXISTING_DEPLOYMENT = EXISTING_DEPLOYMENT % (
        """\
apiVersion: v1
kind: DeploymentConfig""",
    )
    DEPLOYMENT_TYPE = "deploymentconfig"
else:
    KUBECTL = "kubectl"
    EXISTING_DEPLOYMENT = EXISTING_DEPLOYMENT % (
        """\
apiVersion: extensions/v1beta1
kind: Deployment""",
    )
    DEPLOYMENT_TYPE = "deployment"


def random_name(suffix=""):
    """Return a new name each time."""
    if suffix and not suffix.startswith("-"):
        suffix = "-" + suffix
    hostname = socket.gethostname()
    return "testing-{}-{}-{}-{}{}".format(
        REVISION, hostname[:16], os.getpid(), int(time.time() - START_TIME),
        suffix
    ).replace(".", "-")


def telepresence_image_version():
    """Return the version of the Telepresence image we're testing."""
    return os.environ["TELEPRESENCE_VERSION"]


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
            wget --server-response --output-document=output -T3 \
                {url} 2>&1 && break
        done

        # wget output is over.  Put this known string into the output here to
        # separate all that stuff from the response body which comes next.
        echo {delimiter}
        [ -e output ] && cat output
        echo {delimiter}
        """
    ).format(tries=tries, url=url, delimiter=delimiter)
    print(
        "Querying {url} (tries={tries} empty-retries={empty})".format(
            url=url,
            tries=tries,
            empty=retries_on_empty,
        )
    )
    for _ in range(retries_on_empty + 1):
        res = check_output([
            "kubectl",
            "--namespace={}".format(namespace),
            "run",
            random_name("query"),
            "--attach",
            "--quiet",
            "--rm",
            "--image=alpine",
            "--restart=Never",
            "--command",
            "--",
            "sh",
            "-c",
            shell_command,
        ]).decode("utf-8")
        print("query output:")
        print(_indent(res))
        if delimiter in res:
            before, res, after = res.split(delimiter + "\n")
            return res
        print("... empty response (no delimiter)")
    return res


def _indent(text):
    return ">\t" + text.replace("\n", "\n>\t")


def run_webserver(namespace=None):
    """Run webserver in Kubernetes; return Service name."""
    webserver_name = random_name("web")
    if namespace is None:
        namespace = current_namespace()
    kubectl = [KUBECTL, "--namespace", namespace]

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
            query_from_cluster(
                "http://{}:8080/".format(webserver_name), namespace
            )
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
    check_output([KUBECTL, "create", "-f", "-"],
                 input=namespace.encode("utf-8"))


def cleanup_namespace(namespace_name):
    check_call([
        KUBECTL, "delete", "namespace", namespace_name, "--wait=false"
    ])
