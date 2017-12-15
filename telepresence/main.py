"""
Telepresence: local development environment for a remote Kubernetes cluster.
"""

import argparse
import atexit
from copy import deepcopy
import json
import os
import os.path
import re
import signal
import ssl
import sys
from typing import List, Set, Tuple, Dict, Optional, Callable
from functools import wraps
from shutil import rmtree, copy, which
from subprocess import (
    check_output, Popen, CalledProcessError, STDOUT, DEVNULL
)
from tempfile import mkdtemp, NamedTemporaryFile
from time import sleep, time
from traceback import print_exc
from urllib.error import HTTPError
from urllib.request import urlopen
from uuid import uuid4
import webbrowser
from io import StringIO
from urllib.parse import quote_plus

import telepresence
from telepresence.cleanup import kill_process, Subprocesses, wait_for_exit
from telepresence.remote import RemoteInfo, get_remote_info, \
    get_deployment_json
from telepresence.runner import Runner, read_logs
from telepresence.ssh import SSH
from telepresence.usage_tracking import call_scout
from telepresence.utilities import random_name, find_free_port, \
    get_alternate_nameserver
from telepresence.vpn import get_proxy_cidrs, connect_sshuttle

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")
TELEPRESENCE_REMOTE_IMAGE = "{}/telepresence-k8s:{}".format(
    REGISTRY, telepresence.__version__
)
TELEPRESENCE_LOCAL_IMAGE = "{}/telepresence-local:{}".format(
    REGISTRY, telepresence.__version__
)

# IP that shouldn't be in use on Internet, *or* local networks:
MAC_LOOPBACK_IP = "198.18.0.254"

# Whether Docker requires sudo
SUDO_FOR_DOCKER = os.path.exists("/var/run/docker.sock") and not os.access(
    "/var/run/docker.sock", os.W_OK
)


class handle_unexpected_errors(object):
    """Decorator that catches unexpected errors."""

    def __init__(self, logfile):
        self.logfile = logfile

    def __call__(self, f):
        def safe_output(args):
            try:
                return str(check_output(args), "utf-8").strip()
            except Exception as e:
                return "(error: {})".format(e)

        @wraps(f)
        def call_f(*args, **kwargs):
            try:
                return f(*args, **kwargs)
            except SystemExit:
                raise
            except KeyboardInterrupt:
                raise SystemExit(0)
            except Exception as e:
                logs = read_logs(self.logfile)
                errorf = StringIO()
                print_exc(file=errorf)
                error = errorf.getvalue()
                print(
                    "\nLooks like there's a bug in our code. Sorry about that!"
                    "\n\n"
                    "Here's the traceback:\n\n" + error + "\n"
                )
                if self.logfile != "-":
                    print(
                        "And here are the last few lines of the logfile "
                        "(see {} for the complete logs):\n\n".
                        format(self.logfile) +
                        "\n".join(logs.splitlines()[-20:]) + "\n"
                    )

                if input(
                    "Would you like to file an issue in our issue tracker?"
                    " We'd really appreciate the help improving our "
                    "product. [Y/n]: ",
                ).lower() in ("y", ""):
                    url = (
                        "https://github.com/datawire/telepresence/issues/" +
                        "new?body="
                    )
                    body = quote_plus(
                        # Overly long URLs won't work:
                        BUG_REPORT_TEMPLATE.format(
                            sys.argv, telepresence.__version__, sys.version,
                            safe_output([
                                "kubectl", "version", "--short", "--client"
                            ]),
                            safe_output(["oc", "version"]),
                            safe_output(["uname", "-a"]), error, logs[-1000:]
                        )[:4000]
                    )
                    webbrowser.open_new(url + body)
                else:
                    raise SystemExit(1)

        return call_f


HELP_EXAMPLES = """\
== Examples ==

Send a HTTP query to Kubernetes Service called 'myservice' listening on port \
8080:

$ telepresence --run curl http://myservice:8080/

Replace an existing Deployment 'myserver' listening on port 9090 with a local \
process listening on port 9090:

$ telepresence --swap-deployment myserver --expose 9090 \
  --run python3 -m http.server 9090

Use a different local port than the remote port:

$ telepresence --swap-deployment myserver --expose 9090:80 \
  --run python3 -m http.server 9090

Run a Docker container instead of a local process:

$ telepresence --swap-deployment myserver --expose 80 \
  --docker-run -i -t nginx:latest


== Detailed usage ==
"""


@handle_unexpected_errors("-")
def parse_args() -> argparse.Namespace:
    """Create a new ArgumentParser and parse sys.argv."""
    parser = argparse.ArgumentParser(
        formatter_class=argparse.RawDescriptionHelpFormatter,
        allow_abbrev=False,  # can make adding changes not backwards compatible
        description=(
            "Telepresence: local development proxied to a remote Kubernetes "
            "cluster.\n\n"
            "Documentation: https://telepresence.io\n"
            "Real-time help: https://gitter.im/datawire/telepresence\n"
            "Issue tracker: https://github.com/datawire/telepresence/issues\n"
            "\n" + HELP_EXAMPLES + "\n\n"
        )
    )
    parser.add_argument('--version', action='version',
                        version=telepresence.__version__)
    parser.add_argument(
        "--verbose",
        action='store_true',
        help=("Enables verbose logging for troubleshooting.")
    )
    parser.add_argument(
        "--logfile",
        default="./telepresence.log",
        help=(
            "The path to write logs to. '-' means stdout, "
            "default is './telepresence.log'."
        )
    )
    parser.add_argument(
        "--method",
        "-m",
        choices=["inject-tcp", "vpn-tcp", "container"],
        help=(
            "'inject-tcp': inject process-specific shared "
            "library that proxies TCP to the remote cluster.\n"
            "'vpn-tcp': all local processes can route TCP "
            "traffic to the remote cluster. Requires root.\n"
            "'container': used with --docker-run.\n"
            "\n"
            "Default is 'vpn-tcp', or 'container' when --docker-run is used.\n"
            "\nFor more details see "
            "https://telepresence.io/reference/methods.html"
        )
    )
    group_deployment = parser.add_mutually_exclusive_group()
    group_deployment.add_argument(
        '--new-deployment',
        "-n",
        metavar="DEPLOYMENT_NAME",
        dest="new_deployment",
        help=(
            "Create a new Deployment in Kubernetes where the "
            "datawire/telepresence-k8s image will run. It will be deleted "
            "on exit. If no deployment option is specified this will be "
            " used by default, with a randomly generated name."
        )
    )
    group_deployment.add_argument(
        "--swap-deployment",
        "-s",
        dest="swap_deployment",
        metavar="DEPLOYMENT_NAME[:CONTAINER]",
        help=(
            "Swap out an existing deployment with the Telepresence proxy, "
            "swap back on exit. If there are multiple containers in the pod "
            "then add the optional container name to indicate which container"
            " to use."
        )
    )
    group_deployment.add_argument(
        "--deployment",
        "-d",
        metavar="EXISTING_DEPLOYMENT_NAME",
        help=(
            "The name of an existing Kubernetes Deployment where the " +
            "datawire/telepresence-k8s image is already running."
        )
    )
    parser.add_argument(
        "--context",
        default=None,
        help=(
            "The Kubernetes context to use. Defaults to current kubectl"
            " context."
        )
    )
    parser.add_argument(
        "--namespace",
        default=None,
        help=(
            "The Kubernetes namespace to use. Defaults to kubectl's default"
            " for the current context, which is usually 'default'."
        )
    )
    parser.add_argument(
        "--expose",
        action='append',
        metavar="PORT[:REMOTE_PORT]",
        default=[],
        help=(
            "Port number that will be exposed to Kubernetes in the Deployment."
            " Should match port exposed in the existing Deployment if using "
            "--deployment or --swap-deployment. By default local port and "
            "remote port are the same; if you want to listen on port 8080 "
            "locally but be exposed as port 80 in Kubernetes you can do "
            "'--expose 8080:80'."
        )
    )
    parser.add_argument(
        "--also-proxy",
        metavar="CLOUD_HOSTNAME",
        dest="also_proxy",
        action='append',
        default=[],
        help=(
            "If you are using --method=vpn-tcp, use this to add additional "
            "remote IPs or hostnames to proxy. Kubernetes service and pods "
            "are proxied automatically, so you only need to list cloud "
            "resources, e.g. the hostname of a AWS RDS. "
            "When using --method=inject-tcp "
            "this option is unnecessary as all outgoing communication in "
            "the run subprocess will be proxied."
        )
    )
    group = parser.add_mutually_exclusive_group()
    group.add_argument(
        "--run-shell",
        dest="runshell",
        action="store_true",
        help="Run a local shell that will be proxied to/from Kubernetes.",
    )
    group.add_argument(
        "--run",
        metavar=("COMMAND", "ARG"),
        dest="run",
        nargs=argparse.REMAINDER,
        help=(
            "Run the specified command arguments, e.g. "
            "'--run python myapp.py'."
        )
    )
    group.add_argument(
        "--docker-run",
        metavar="DOCKER_RUN_ARG",
        dest="docker_run",
        nargs=argparse.REMAINDER,
        help=(
            "Run a Docker container, by passing the arguments to 'docker run',"
            " e.g. '--docker-run -i -t ubuntu:16.04 /bin/bash'. "
            "Requires --method container."
        )
    )
    args = parser.parse_args()

    # Fill in defaults:
    if args.method is None:
        if args.docker_run is not None:
            args.method = "container"
        else:
            args.method = "vpn-tcp"
    if args.deployment is None and args.new_deployment is None and (
        args.swap_deployment is None
    ):
        args.new_deployment = random_name()

    if args.method == "container" and args.docker_run is None:
        raise SystemExit(
            "'--docker-run' is required when using '--method container'."
        )
    if args.docker_run is not None and args.method != "container":
        raise SystemExit(
            "'--method container' is required when using '--docker-run'."
        )

    args.expose = PortMapping.parse(args.expose)
    return args


class PortMapping(object):
    """Maps local ports to listen to remote exposed ports."""

    def __init__(self):
        self._mapping = {}  # type: Dict[int,int]

    @classmethod
    def parse(cls, port_strings: List[str]):
        """Parse list of 'port' or 'local_port:remote_port' to PortMapping."""
        result = PortMapping()
        for port_string in port_strings:
            if ":" in port_string:
                local_port, remote_port = map(int, port_string.split(":"))
            else:
                local_port, remote_port = int(port_string), int(port_string)
            result._mapping[local_port] = remote_port
        return result

    def merge_automatic_ports(self, ports: List[int]):
        """
        Merge a list of ports to the existing ones.

        The existing ones will win if the port is already there.
        """
        remote = self.remote()
        for port in ports:
            if port in remote:
                continue
            self._mapping[port] = port

    def remote(self) -> Set[int]:
        """Return set of remote ports."""
        return set(self._mapping.values())

    def local_to_remote(self) -> Set[Tuple[int, int]]:
        """Return set of pairs of local, remote ports."""
        return set(self._mapping.items())


def _get_remote_env(
    runner: Runner,
    context: str,
    namespace: str,
    pod_name: str,
    container_name: str
) -> Dict[str, str]:
    """Get the environment variables in the remote pod."""
    env = runner.get_kubectl(
        context, namespace,
        ["exec", pod_name, "--container", container_name, "env"]
    )
    result = {}  # type: Dict[str,str]
    prior_key = None
    for line in env.splitlines():
        try:
            key, value = line.split("=", 1)
            prior_key = key
        except ValueError:
            # Prior key's value contains one or more newlines
            key = prior_key
            value = result[key] + "\n" + line
        result[key] = value
    return result


def get_env_variables(runner: Runner, remote_info: RemoteInfo,
                      context: str) -> Dict[str, str]:
    """
    Generate environment variables that match kubernetes.
    """
    # Get the environment:
    remote_env = _get_remote_env(
        runner, context, remote_info.namespace, remote_info.pod_name,
        remote_info.container_name
    )
    # Tell local process about the remote setup, useful for testing and
    # debugging:
    result = {
        "TELEPRESENCE_POD": remote_info.pod_name,
        "TELEPRESENCE_CONTAINER": remote_info.container_name
    }
    # Alpine, which we use for telepresence-k8s image, automatically sets these
    # HOME, PATH, HOSTNAME. The rest are from Kubernetes:
    for key in ("HOME", "PATH", "HOSTNAME"):
        if key in remote_env:
            del remote_env[key]
    result.update(remote_env)
    return result


def expose_local_services(
    processes: Subprocesses, ssh: SSH, port_numbers: List[Tuple[int, int]]
) -> None:
    """Create SSH tunnels from remote proxy pod to local host.

    :param processes: A `Subprocesses` instance.
    :param ssh: A 'SSH` instance.
    :param port_numbers: List of pairs of (local port, remote port).
    """
    output = sys.stderr.isatty()
    if not port_numbers and output:
        print(
            "No traffic is being forwarded from the remote Deployment to your"
            " local machine. You can use the --expose option to specify which"
            " ports you want to forward.",
            file=sys.stderr
        )
    for local_port, remote_port in port_numbers:
        if output:
            print(
                "Forwarding remote port {} to local port {}.".format(
                    remote_port,
                    local_port,
                ),
                file=sys.stderr
            )
        processes.append(
            ssh.
            popen(["-R", "*:{}:127.0.0.1:{}".format(remote_port, local_port)])
        )
    if output:
        print("", file=sys.stderr)


def connect(
    runner: Runner, remote_info: RemoteInfo, cmdline_args: argparse.Namespace
) -> Tuple[Subprocesses, int, SSH]:
    """
    Start all the processes that handle remote proxying.

    Return (Subprocesses, local port of SOCKS proxying tunnel, SSH instance).
    """
    processes = Subprocesses()
    # Keep local copy of pod logs, for debugging purposes:
    processes.append(
        runner.popen(
            runner.kubectl(
                cmdline_args.context, remote_info.namespace, [
                    "logs", "-f", remote_info.pod_name, "--container",
                    remote_info.container_name
                ]
            ),
            bufsize=0,
        )
    )

    ssh = SSH(runner, find_free_port())

    # forward remote port to here, by tunneling via remote SSH server:
    processes.append(
        runner.popen(
            runner.kubectl(
                cmdline_args.context, remote_info.namespace, [
                    "port-forward", remote_info.pod_name,
                    "{}:8022".format(ssh.port)
                ]
            )
        )
    )
    if cmdline_args.method == "container":
        # kubectl port-forward currently only listens on loopback. So we
        # portforward from the docker0 interface on Linux, and the lo0 alias we
        # added on OS X, to loopback (until we can use kubectl port-forward
        # option to listen on docker0 -
        # https://github.com/kubernetes/kubernetes/pull/46517, or all our users
        # have latest version of Docker for Mac, which has nicer solution -
        # https://github.com/datawire/telepresence/issues/224).
        if sys.platform == "linux":

            # If ip addr is available use it if not fall back to ifconfig.
            if which("ip"):
                docker_interfaces = re.findall(
                    r"(\d+\.\d+\.\d+\.\d+)",
                    runner.get_output(["ip", "addr", "show", "dev", "docker0"])
                )
            elif which("ifconfig"):
                docker_interfaces = re.findall(
                    r"(\d+\.\d+\.\d+\.\d+)",
                    runner.get_output(["ifconfig", "docker0"])
                )
            else:
                raise SystemExit("'ip addr' nor 'ifconfig' available")

            if len(docker_interfaces) == 0:
                raise SystemExit("No interface for docker found")

            docker_interface = docker_interfaces[0]

        else:
            # The way to get routing from container to host is via an alias on
            # lo0 (https://docs.docker.com/docker-for-mac/networking/). We use
            # an IP range that is assigned for testing network devices and
            # therefore shouldn't conflict with real IPs or local private
            # networks (https://tools.ietf.org/html/rfc6890).
            runner.check_call([
                "sudo", "ifconfig", "lo0", "alias", MAC_LOOPBACK_IP
            ])
            atexit.register(
                runner.check_call,
                ["sudo", "ifconfig", "lo0", "-alias", MAC_LOOPBACK_IP]
            )
            docker_interface = MAC_LOOPBACK_IP
        processes.append(
            runner.popen([
                "socat", "TCP4-LISTEN:{},bind={},reuseaddr,fork".format(
                    ssh.port,
                    docker_interface,
                ), "TCP4:127.0.0.1:{}".format(ssh.port)
            ])
        )

    ssh.wait()

    # In Docker mode this happens inside the local Docker container:
    if cmdline_args.method != "container":
        expose_local_services(
            processes,
            ssh,
            cmdline_args.expose.local_to_remote(),
        )

    socks_port = find_free_port()
    if cmdline_args.method == "inject-tcp":
        # start tunnel to remote SOCKS proxy:
        processes.append(
            ssh.popen(["-L",
                       "127.0.0.1:{}:127.0.0.1:9050".format(socks_port)]),
        )

    return processes, socks_port, ssh


def create_new_deployment(runner: Runner,
                          args: argparse.Namespace) -> Tuple[str, str]:
    """Create a new Deployment, return its name and Kubernetes label."""
    run_id = str(uuid4())

    def remove_existing_deployment():
        runner.get_kubectl(
            args.context, args.namespace, [
                "delete",
                "--ignore-not-found",
                "all",
                "--selector=telepresence=" + run_id,
            ]
        )

    atexit.register(remove_existing_deployment)
    remove_existing_deployment()
    command = [
        "run",
        # This will result in using Deployment:
        "--restart=Always",
        "--limits=cpu=100m,memory=256Mi",
        "--requests=cpu=25m,memory=64Mi",
        args.new_deployment,
        "--image=" + TELEPRESENCE_REMOTE_IMAGE,
        "--labels=telepresence=" + run_id,
    ]
    for port in args.expose.remote():
        command.append("--port={}".format(port))
    if args.expose.remote():
        command.append("--expose")
    # If we're on local VM we need to use different nameserver to prevent
    # infinite loops caused by sshuttle:
    if args.method == "vpn-tcp" and args.in_local_vm:
        command.append(
            "--env=TELEPRESENCE_NAMESERVER=" + get_alternate_nameserver()
        )
    if args.needs_root:
        override = {
            "apiVersion": "extensions/v1beta1",
            "spec": {
                "template": {
                    "spec": {
                        "securityContext": {
                            "runAsUser": 0
                        }
                    }
                }
            }
        }
        command.append("--overrides=" + json.dumps(override))
    runner.get_kubectl(args.context, args.namespace, command)
    return args.new_deployment, run_id


def swap_deployment(runner: Runner,
                    args: argparse.Namespace) -> Tuple[str, str, Dict]:
    """
    Swap out an existing Deployment.

    Native Kubernetes version.

    Returns (Deployment name, unique K8s label, JSON of original container that
    was swapped out.)
    """
    run_id = str(uuid4())

    deployment_name, *container_name = args.swap_deployment.split(":", 1)
    if container_name:
        container_name = container_name[0]
    deployment_json = get_deployment_json(
        runner,
        deployment_name,
        args.context,
        args.namespace,
        "deployment",
    )

    def apply_json(json_config):
        # apply without delete will merge in unexpected ways, e.g. missing
        # container attributes in the pod spec will not be removed. so we
        # delete and then recreate.
        runner.check_kubectl(
            args.context,
            args.namespace, ["delete", "deployment", deployment_name]
        )
        runner.check_kubectl(
            args.context,
            args.namespace, ["apply", "-f", "-"],
            input=json.dumps(json_config).encode("utf-8")
        )

    atexit.register(apply_json, deployment_json)

    # If no container name was given, just use the first one:
    if not container_name:
        container_name = deployment_json["spec"]["template"]["spec"][
            "containers"
        ][0]["name"]

    # If we're on local VM we need to use different nameserver to
    # prevent infinite loops caused by sshuttle.
    new_deployment_json, orig_container_json = new_swapped_deployment(
        deployment_json,
        container_name,
        run_id,
        TELEPRESENCE_REMOTE_IMAGE,
        args.method == "vpn-tcp" and args.in_local_vm,
        args.needs_root,
    )
    apply_json(new_deployment_json)
    return deployment_name, run_id, orig_container_json


def new_swapped_deployment(
    old_deployment: Dict,
    container_to_update: str,
    run_id: str,
    telepresence_image: str,
    add_custom_nameserver: bool,
    as_root: bool,
) -> Tuple[Dict, Dict]:
    """
    Create a new Deployment that uses telepresence-k8s image.

    Makes the following changes:

    1. Changes to single replica.
    2. Disables command, args, livenessProbe, readinessProbe, workingDir.
    3. Adds labels.
    4. Adds TELEPRESENCE_NAMESERVER env variable, if requested.
    5. Runs as root, if requested.
    6. Sets terminationMessagePolicy.
    7. Adds TELEPRESENCE_CONTAINER_NAMESPACE env variable so the forwarder does
       not have to access the k8s API from within the pod.

    Returns dictionary that can be encoded to JSON and used with kubectl apply,
    and contents of swapped out container.
    """
    new_deployment_json = deepcopy(old_deployment)
    new_deployment_json["spec"]["replicas"] = 1
    new_deployment_json["metadata"].setdefault("labels",
                                               {})["telepresence"] = run_id
    new_deployment_json["spec"]["template"]["metadata"].setdefault(
        "labels", {}
    )["telepresence"] = run_id
    for container, old_container in zip(
        new_deployment_json["spec"]["template"]["spec"]["containers"],
        old_deployment["spec"]["template"]["spec"]["containers"],
    ):
        if container["name"] == container_to_update:
            container["image"] = telepresence_image
            # Not strictly necessary for real use, but tests break without this
            # since we don't upload test images to Docker Hub:
            container["imagePullPolicy"] = "IfNotPresent"
            # Drop unneeded fields:
            for unneeded in [
                "command", "args", "livenessProbe", "readinessProbe",
                "workingDir"
            ]:
                try:
                    container.pop(unneeded)
                except KeyError:
                    pass
            # We don't write out termination file:
            container["terminationMessagePolicy"] = "FallbackToLogsOnError"
            # Use custom name server if necessary:
            if add_custom_nameserver:
                container.setdefault("env", []).append({
                    "name":
                    "TELEPRESENCE_NAMESERVER",
                    "value":
                    get_alternate_nameserver()
                })
            if as_root:
                container["securityContext"] = {
                    "runAsUser": 0,
                }
            # Add namespace environment variable to support deployments using
            # automountServiceAccountToken: false. To be used by forwarder.py
            # in the k8s-proxy.
            container.setdefault("env", []).append({
                    "name": "TELEPRESENCE_CONTAINER_NAMESPACE",
                    "valueFrom": {
                        "fieldRef": {
                            "fieldPath": "metadata.namespace"
                        }
                    }
                })
            return new_deployment_json, old_container

    raise RuntimeError(
        "Couldn't find container {} in the Deployment.".
        format(container_to_update)
    )


def swap_deployment_openshift(runner: Runner, args: argparse.Namespace
                              ) -> Tuple[str, str, Dict]:
    """
    Swap out an existing DeploymentConfig.

    Returns (Deployment name, unique K8s label, JSON of original container that
    was swapped out.)

    In practice OpenShift doesn't seem to do the right thing when a
    DeploymentConfig is updated. In particular, we need to disable the image
    trigger so that we can use the new image, but the replicationcontroller
    then continues to deploy the existing image.

    So instead we use a different approach than for Kubernetes, replacing the
    current ReplicationController with one that uses the Telepresence image,
    then restores it. We delete the pods to force the RC to do its thing.
    """
    run_id = str(uuid4())
    deployment_name, *container_name = args.swap_deployment.split(":", 1)
    if container_name:
        container_name = container_name[0]
    rcs = runner.get_kubectl(
        args.context, args.namespace, [
            "get", "rc", "-o", "name", "--selector",
            "openshift.io/deployment-config.name=" + deployment_name
        ]
    )
    rc_name = sorted(
        rcs.split(), key=lambda n: int(n.split("-")[-1])
    )[0].split("/", 1)[1]
    rc_json = json.loads(
        runner.get_kubectl(
            args.context,
            args.namespace, ["get", "rc", "-o", "json", "--export", rc_name],
            stderr=STDOUT
        )
    )

    def apply_json(json_config):
        runner.check_kubectl(
            args.context,
            args.namespace, ["apply", "-f", "-"],
            input=json.dumps(json_config).encode("utf-8")
        )
        # Now that we've updated the replication controller, delete pods to
        # make sure changes get applied:
        runner.check_kubectl(
            args.context, args.namespace,
            ["delete", "pod", "--selector", "deployment=" + rc_name]
        )

    atexit.register(apply_json, rc_json)

    # If no container name was given, just use the first one:
    if not container_name:
        container_name = rc_json["spec"]["template"]["spec"]["containers"
                                                             ][0]["name"]

    new_rc_json, orig_container_json = new_swapped_deployment(
        rc_json, container_name, run_id, TELEPRESENCE_REMOTE_IMAGE,
        args.method == "vpn-tcp" and args.in_local_vm, False,
    )
    apply_json(new_rc_json)
    return deployment_name, run_id, orig_container_json


def start_proxy(runner: Runner, args: argparse.Namespace
                ) -> Tuple[Subprocesses, Dict[str, str], int, SSH, RemoteInfo]:
    """Start the kubectl port-forward and SSH clients that do the proxying."""
    if sys.stdout.isatty() and args.method != "container":
        print(
            "Starting proxy with method '{}', which has the following "
            "limitations:".format(args.method),
            file=sys.stderr,
            end=" ",
        )
        if args.method == "vpn-tcp":
            print(
                "All processes are affected, only one telepresence"
                " can run per machine, and you can't use other VPNs."
                " You may need to add cloud hosts with --also-proxy.",
                file=sys.stderr,
                end=" ",
            )
        elif args.method == "inject-tcp":
            print(
                "Go programs, static binaries, suid programs, and custom DNS"
                " implementations are not supported.",
                file=sys.stderr,
                end=" ",
            )
        print(
            "For a full list of method limitations see "
            "https://telepresence.io/reference/methods.html",
            file=sys.stderr
        )
    if sys.stdout.isatty():
        print(
            "Volumes are rooted at $TELEPRESENCE_ROOT. See "
            "https://telepresence.io/howto/volumes.html for details.\n",
            file=sys.stderr
        )

    run_id = None

    if args.new_deployment is not None:
        # This implies --new-deployment:
        args.deployment, run_id = create_new_deployment(runner, args)

    if args.swap_deployment is not None:
        # This implies --swap-deployment
        if runner.kubectl_cmd == "oc":
            args.deployment, run_id, container_json = (
                swap_deployment_openshift(runner, args)
            )
        else:
            args.deployment, run_id, container_json = swap_deployment(
                runner, args
            )
        args.expose.merge_automatic_ports([
            p["containerPort"] for p in container_json.get("ports", [])
            if p["protocol"] == "TCP"
        ])

    deployment_type = "deployment"
    if runner.kubectl_cmd == "oc":
        # OpenShift Origin uses DeploymentConfig instead, but for swapping we
        # mess with RweplicationController instead because mutating DC doesn't
        # work:
        if args.swap_deployment:
            deployment_type = "rc"
        else:
            deployment_type = "deploymentconfig"

    remote_info = get_remote_info(
        runner,
        args.deployment,
        args.context,
        args.namespace,
        deployment_type,
        run_id=run_id,
    )

    processes, socks_port, ssh = connect(runner, remote_info, args)

    # Get the environment variables we want to copy from the remote pod; it may
    # take a few seconds for the SSH proxies to get going:
    start = time()
    while time() - start < 10:
        try:
            env = get_env_variables(runner, remote_info, args.context)
            break
        except CalledProcessError:
            sleep(0.25)

    return processes, env, socks_port, ssh, remote_info


TORSOCKS_CONFIG = """
# Allow process to listen on ports:
AllowInbound 1
# Allow process to connect to localhost:
AllowOutboundLocalhost 1
# Connect to custom port for SOCKS server:
TorPort {}
"""


def sip_workaround(existing_paths: str, unsupported_tools_path: str) -> str:
    """
    Workaround System Integrity Protection.

    Newer OS X don't allow injecting libraries into binaries in /bin, /sbin and
    /usr. We therefore make a copy of them and modify $PATH to point at their
    new location. It's only ~100MB so this should be pretty fast!

    :param existing_paths: Current $PATH.
    :param unsupported_tools_path: Path where we have custom versions of ping
        etc. Needs to be first in list so that we override system versions.
    """
    protected = {"/bin", "/sbin", "/usr/sbin", "/usr/bin"}
    # Remove protected paths from $PATH:
    paths = [p for p in existing_paths.split(":") if p not in protected]
    # Add temp dir
    bin_dir = mkdtemp(dir="/tmp")
    paths.insert(0, bin_dir)
    atexit.register(rmtree, bin_dir)
    for directory in protected:
        for file in os.listdir(directory):
            try:
                copy(os.path.join(directory, file), bin_dir)
            except IOError:
                continue
            os.chmod(os.path.join(bin_dir, file), 0o775)
    paths = [unsupported_tools_path] + paths
    # Return new $PATH
    return ":".join(paths)


def mount_remote_volumes(
    runner: Runner, remote_info: RemoteInfo, ssh: SSH, allow_all_users: bool
) -> Tuple[str, Callable]:
    """
    sshfs is used to mount the remote system locally.

    Allowing all users may require root, so we use sudo in that case.

    Returns (path to mounted directory, callable that will unmount it).
    """
    # Docker for Mac only shares some folders; the default TMPDIR on OS X is
    # not one of them, so make sure we use /tmp:
    mount_dir = mkdtemp(dir="/tmp")
    sudo_prefix = ["sudo"] if allow_all_users else []
    middle = ["-o", "allow_other"] if allow_all_users else []
    try:
        runner.check_call(
            sudo_prefix + [
                "sshfs",
                "-p",
                str(ssh.port),
                # Don't load config file so it doesn't break us:
                "-F",
                "/dev/null",
                # Don't validate host key:
                "-o",
                "StrictHostKeyChecking=no",
                # Don't store host key:
                "-o",
                "UserKnownHostsFile=/dev/null",
            ] + middle + ["telepresence@localhost:/", mount_dir]
        )
        mounted = True
    except CalledProcessError:
        print(
            "Mounting remote volumes failed, they will be unavailable"
            " in this session. If you are running"
            " on Windows Subystem for Linux then see"
            " https://github.com/datawire/telepresence/issues/115,"
            " otherwise please report a bug, attaching telepresence.log to"
            " the bug report:"
            " https://github.com/datawire/telepresence/issues/new",
            file=sys.stderr
        )
        mounted = False

    def no_cleanup():
        pass

    def cleanup():
        if sys.platform.startswith("linux"):
            runner.check_call(
                sudo_prefix + ["fusermount", "-z", "-u", mount_dir]
            )
        else:
            runner.get_output(sudo_prefix + ["umount", "-f", mount_dir])

    return mount_dir, cleanup if mounted else no_cleanup


NICE_FAILURE = """\
#!/bin/sh
echo {} is not supported under Telepresence.
echo See \
https://telepresence.io/reference/limitations.html \
for details.
exit 55
"""


def get_unsupported_tools(dns_supported: bool) -> str:
    """
    Create replacement command-line tools that just error out, in a nice way.

    Returns path to directory where overriden tools are stored.
    """
    commands = ["ping", "traceroute"]
    if not dns_supported:
        commands += ["nslookup", "dig", "host"]
    unsupported_bin = mkdtemp(dir="/tmp")
    for command in commands:
        path = unsupported_bin + "/" + command
        with open(path, "w") as f:
            f.write(NICE_FAILURE.format(command))
        os.chmod(path, 0o755)
    return unsupported_bin


def docker_runify(args: List[str]) -> List[str]:
    """Prepend 'docker run' to a list of arguments."""
    args = ['docker', 'run'] + args
    if SUDO_FOR_DOCKER:
        return ["sudo"] + args
    else:
        return args


def make_docker_kill(runner: Runner, name: str) -> Callable:
    """Return a function that will kill a named docker container."""

    def kill():
        sudo = ["sudo"] if SUDO_FOR_DOCKER else []
        runner.check_call(sudo + ["docker", "stop", "--time=1", name])

    return kill


def run_docker_command(
    runner: Runner,
    remote_info: RemoteInfo,
    args: argparse.Namespace,
    remote_env: Dict[str, str],
    subprocesses: Subprocesses,
    ssh: SSH,
) -> None:
    """
    --docker-run support.

    Connect using sshuttle running in a Docker container, and then run user
    container.

    :param args: Command-line args to telepresence binary.
    :param remote_env: Dictionary with environment on remote pod.
    :param mount_dir: Path to local directory where remote pod's filesystem is
        mounted.
    """
    # Mount remote filesystem. We allow all users if we're using Docker because
    # we don't know what uid the Docker container will use:
    mount_dir, mount_cleanup = mount_remote_volumes(
        runner,
        remote_info,
        ssh,
        True,
    )

    # Update environment:
    remote_env["TELEPRESENCE_ROOT"] = mount_dir
    remote_env["TELEPRESENCE_METHOD"] = "container"  # mostly just for tests :(

    # Start the sshuttle container:
    name = random_name()
    config = {
        "port":
        ssh.port,
        "cidrs":
        get_proxy_cidrs(
            runner, args, remote_info, remote_env["KUBERNETES_SERVICE_HOST"]
        ),
        "expose_ports":
        list(args.expose.local_to_remote()),
    }
    if sys.platform == "darwin":
        config["ip"] = MAC_LOOPBACK_IP
    # Image already has tini init so doesn't need --init option:
    subprocesses.append(
        runner.popen(
            docker_runify([
                "--rm", "--privileged", "--name=" + name,
                TELEPRESENCE_LOCAL_IMAGE, "proxy", json.dumps(config)
            ])
        ), make_docker_kill(runner, name)
    )

    # Write out env file:
    with NamedTemporaryFile("w", delete=False) as envfile:
        for key, value in remote_env.items():
            envfile.write("{}={}\n".format(key, value))
    atexit.register(os.remove, envfile.name)

    # Wait for sshuttle to be running:
    while True:
        try:
            runner.check_call(
                docker_runify([
                    "--network=container:" + name, "--rm",
                    TELEPRESENCE_LOCAL_IMAGE, "wait"
                ])
            )
        except CalledProcessError as e:
            if e.returncode == 100:
                # We're good!
                break
                return name, envfile.name
            elif e.returncode == 125:
                # Docker failure, probably due to original container not
                # starting yet... so sleep and try again:
                sleep(1)
                continue
            else:
                raise
        else:
            raise RuntimeError(
                "Waiting container exited prematurely. File a bug, please!"
            )

    # Start the container specified by the user:
    container_name = random_name()
    docker_command = docker_runify([
        "--volume={}:{}".format(mount_dir, mount_dir),
        "--name=" + container_name,
        "--network=container:" + name,
        "--env-file",
        envfile.name,
    ])
    # Older versions of Docker don't have --init:
    if "--init" in runner.get_output(["docker", "run", "--help"]):
        docker_command += ["--init"]
    docker_command += args.docker_run
    p = Popen(docker_command)

    def terminate_if_alive():
        runner.write("Shutting down containers...\n")
        if p.poll() is None:
            runner.write("Killing local container...\n")
            make_docker_kill(runner, container_name)()

        mount_cleanup()

    atexit.register(terminate_if_alive)
    wait_for_exit(runner, p, subprocesses)


def setup_torsocks(runner, env, socks_port, unsupported_tools_path):
    """Setup environment variables to make torsocks work correctly."""
    # Create custom torsocks.conf, since some options we want (in particular,
    # port) aren't accessible via env variables in older versions of torconf:
    with NamedTemporaryFile(mode="w+", delete=False) as tor_conffile:
        tor_conffile.write(TORSOCKS_CONFIG.format(socks_port))
    atexit.register(os.remove, tor_conffile.name)
    env["TORSOCKS_CONF_FILE"] = tor_conffile.name
    if runner.logfile is not sys.stdout:
        env["TORSOCKS_LOG_FILE_PATH"] = runner.logfile.name
    if sys.platform == "darwin":
        env["PATH"] = sip_workaround(env["PATH"], unsupported_tools_path)
    # Try to ensure we're actually proxying network, by forcing DNS resolution
    # via torsocks:
    start = time()
    while time() - start < 10:
        try:
            runner.check_call([
                "torsocks", "python3", "-c",
                "import socket; socket.socket().connect(('google.com', 80))"
            ],
                              env=env)
        except CalledProcessError:
            sleep(0.1)
        else:
            return
    raise RuntimeError("SOCKS network proxying failed to start...")


def run_local_command(
    runner: Runner,
    remote_info: RemoteInfo,
    args: argparse.Namespace,
    env_overrides: Dict[str, str],
    subprocesses: Subprocesses,
    socks_port: int,
    ssh: SSH
) -> None:
    """--run-shell/--run support, run command locally."""
    env = os.environ.copy()
    env.update(env_overrides)

    # Don't use runner.popen() since we want to give program access to current
    # stdout and stderr if it wants it.
    env["PROMPT_COMMAND"] = (
        'PS1="@{}|$PS1";unset PROMPT_COMMAND'.format(args.context)
    )

    # Inject replacements for unsupported tools like ping:
    unsupported_tools_path = get_unsupported_tools(args.method != "inject-tcp")
    env["PATH"] = unsupported_tools_path + ":" + env["PATH"]

    # Mount remote filesystem:
    mount_dir, mount_cleanup = mount_remote_volumes(
        runner, remote_info, ssh, False
    )
    env["TELEPRESENCE_ROOT"] = mount_dir

    # Make sure we use "bash", no "/bin/bash", so we get the copied version on
    # OS X:
    if args.run is None:
        # We skip .bashrc since it might e.g. have kubectl running to get bash
        # autocomplete, and Go programs don't like DYLD on macOS at least (see
        # https://github.com/datawire/telepresence/issues/125).
        command = ["bash", "--norc"]
    else:
        command = args.run
    if args.method == "inject-tcp":
        setup_torsocks(runner, env, socks_port, unsupported_tools_path)
        p = Popen(["torsocks"] + command, env=env)
    elif args.method == "vpn-tcp":
        connect_sshuttle(runner, remote_info, args, subprocesses, env, ssh)
        p = Popen(command, env=env)

    def terminate_if_alive():
        runner.write("Shutting down local process...\n")
        if p.poll() is None:
            runner.write("Killing local process...\n")
            kill_process(p)

        mount_cleanup()

    atexit.register(terminate_if_alive)
    wait_for_exit(runner, p, subprocesses)


BUG_REPORT_TEMPLATE = u"""\
### What were you trying to do?

(please tell us)

### What did you expect to happen?

(please tell us)

### What happened instead?

(please tell us - the traceback is automatically included, see below)

### Automatically included information

Command line: `{}`
Version: `{}`
Python version: `{}`
kubectl version: `{}`
oc version: `{}`
OS: `{}`
Traceback:

```
{}
```

Logs:

```
{}
```
"""


def require_command(runner: Runner, command: str, message: Optional[str]=None):
    if message is None:
        message = "Please install " + command
    try:
        runner.get_output(["which", command])
    except CalledProcessError as e:
        sys.stderr.write(message + "\n")
        sys.stderr.write(
            '(Ran "which {}" to check in your $PATH.)\n'.format(command)
        )
        sys.stderr.write(
            "See the documentation at https://telepresence.io "
            "for more details.\n"
        )
        raise SystemExit(1)


def kubectl_or_oc(server: str) -> str:
    """
    Return "kubectl" or "oc", the command-line tool we should use.

    :param server: The URL of the cluster API server.
    """
    if which("oc") is None:
        return "kubectl"
    # We've got oc, and possibly kubectl as well. We only want oc for OpenShift
    # servers, so check for an OpenShift API endpoint:
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        with urlopen(server + "/version/openshift", context=ctx) as u:
            u.read()
    except HTTPError:
        return "kubectl"
    else:
        return "oc"


def main():
    # Make SIGTERM and SIGHUP do clean shutdown (in particular, we want atexit
    # functions to be called):
    def shutdown(signum, frame):
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGHUP, shutdown)

    args = parse_args()

    @handle_unexpected_errors(args.logfile)
    def go():
        # We don't quite know yet if we want kubectl or oc (if someone has both
        # it depends on the context), so until we know the context just guess.
        # We prefer kubectl over oc insofar as (1) kubectl commands we do in
        # this prelim setup stage don't require oc and (2) sometimes oc is a
        # different binary unrelated to OpenShift.
        if which("kubectl"):
            prelim_command = "kubectl"
        elif which("oc"):
            prelim_command = "oc"
        else:
            raise SystemExit("Found neither 'kubectl' nor 'oc' in your $PATH.")

        # Usage tracking
        try:
            kubectl_version_output = str(
                check_output([prelim_command, "version", "--short"]), "utf-8"
            ).split("\n")
            kubectl_version = kubectl_version_output[0].split(": v")[1]
            kube_cluster_version = kubectl_version_output[1].split(": v")[1]
        except CalledProcessError as exc:
            kubectl_version = kube_cluster_version = "(error: {})".format(exc)
        if args.deployment:
            operation = "deployment"
        elif args.new_deployment:
            operation = "new_deployment"
        elif args.swap_deployment:
            operation = "swap_deployment"
        else:
            operation = "bad_args"
        scouted = call_scout(
            kubectl_version,
            kube_cluster_version,
            operation,
            args.method
        )

        # Make sure we have a Kubernetes context set either on command line or
        # in kubeconfig:
        if args.context is None:
            try:
                args.context = str(
                    check_output([prelim_command, "config", "current-context"],
                                 stderr=STDOUT), "utf-8"
                ).strip()
            except CalledProcessError:
                raise SystemExit(
                    "No current-context set. "
                    "Please use the --context option to explicitly set the "
                    "context."
                )

        # Figure out explicit namespace if its not specified, and the server
        # address (we use the server address to determine for good whether we
        # want oc or kubectl):
        kubectl_config = json.loads(
            str(
                check_output([prelim_command, "config", "view", "-o", "json"]),
                "utf-8"
            )
        )
        for context_setting in kubectl_config["contexts"]:
            if context_setting["name"] == args.context:
                if args.namespace is None:
                    args.namespace = context_setting["context"].get(
                        "namespace", "default"
                    )
                cluster = context_setting["context"]["cluster"]
                break
        for cluster_setting in kubectl_config["clusters"]:
            if cluster_setting["name"] == cluster:
                server = cluster_setting["cluster"]["server"]

        # Log file path should be absolute since some processes may run in
        # different directories:
        if args.logfile != "-":
            args.logfile = os.path.abspath(args.logfile)
        runner = Runner.open(args.logfile, kubectl_or_oc(server), args.verbose)
        runner.write("Scout info: {}\n".format(scouted))
        runner.write(
            "Context: {}, namespace: {}, kubectl_command: {}\n".
            format(args.context, args.namespace, runner.kubectl_cmd)
        )

        # Figure out if we need capability that allows for ports < 1024:
        if any([p < 1024 for p in args.expose.remote()]):
            if runner.kubectl_cmd == "oc":
                # OpenShift doesn't support running as root:
                raise SystemExit("OpenShift does not support ports <1024.")
            args.needs_root = True
        else:
            args.needs_root = False

        # minikube/minishift break DNS because DNS gets captured, sent to
        # minikube, which sends it back to DNS server set by host, resulting in
        # loop... we've fixed that for most cases, but not --deployment.
        def check_if_in_local_vm() -> bool:
            # Minikube just has 'minikube' as context'
            if args.context == "minikube":
                return True
            # Minishift has complex context name, so check by server:
            if runner.kubectl_cmd == "oc" and which("minishift"):
                ip = runner.get_output(["minishift", "ip"]).strip()
                if ip and ip in server:
                    return True
            return False

        args.in_local_vm = check_if_in_local_vm()
        if args.in_local_vm:
            runner.write("Looks like we're in a local VM, e.g. minikube.\n")
        if (
                args.in_local_vm and args.method == "vpn-tcp" and
                args.new_deployment is None and args.swap_deployment is None
        ):
            raise SystemExit(
                "vpn-tcp method doesn't work with minikube/minishift when"
                " using --deployment. Use --swap-deployment or"
                " --new-deployment instead."
            )

        # Make sure we can access Kubernetes:
        try:
            if runner.kubectl_cmd == "oc":
                status_command = "status"
            else:
                status_command = "cluster-info"
            runner.get_output([
                runner.kubectl_cmd, "--context", args.context, status_command
            ])
        except (CalledProcessError, OSError, IOError) as e:
            sys.stderr.write("Error accessing Kubernetes: {}\n".format(e))
            raise SystemExit(1)
        # Make sure we can run openssh:
        try:
            version = runner.get_output(
                ["ssh", "-V"],
                stdin=DEVNULL,
                stderr=STDOUT
            )
            if not version.startswith("OpenSSH"):
                raise SystemExit(
                    "'ssh' is not the OpenSSH client, apparently."
                )
        except (CalledProcessError, OSError, IOError) as e:
            sys.stderr.write("Error running ssh: {}\n".format(e))
            raise SystemExit(1)

        # Other requirements:
        require_command(
            runner, "torsocks", "Please install torsocks (v2.1 or later)"
        )
        require_command(runner, "sshfs")
        # Need conntrack for sshuttle on Linux:
        if sys.platform.startswith("linux") and args.method == "vpn-tcp":
            require_command(runner, "conntrack")

        subprocesses, env, socks_port, ssh, remote_info = start_proxy(
            runner, args
        )
        if args.method == "container":
            run_docker_command(
                runner,
                remote_info,
                args,
                env,
                subprocesses,
                ssh,
            )
        else:
            run_local_command(
                runner, remote_info, args, env, subprocesses, socks_port, ssh
            )

    go()


def run_telepresence():
    """Run telepresence"""
    if sys.version_info[:2] < (3, 5):
        raise SystemExit("Telepresence requires Python 3.5 or later.")
    main()


if __name__ == '__main__':
    run_telepresence()
