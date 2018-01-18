"""
Telepresence: local development environment for a remote Kubernetes cluster.
"""

import argparse
import atexit
import json
import signal

import os
import re
import sys
from typing import List, Tuple, Dict
from shutil import which
from subprocess import (CalledProcessError, check_output, STDOUT, DEVNULL)
from time import sleep, time

from telepresence.cleanup import Subprocesses
from telepresence.cli import parse_args, handle_unexpected_errors
from telepresence.deployment import create_new_deployment, swap_deployment, \
    swap_deployment_openshift
from telepresence.container import MAC_LOOPBACK_IP, run_docker_command
from telepresence.local import run_local_command
from telepresence.remote import RemoteInfo, get_remote_info
from telepresence.runner import Runner
from telepresence.ssh import SSH
from telepresence.startup import kubectl_or_oc, require_command
from telepresence.usage_tracking import call_scout
from telepresence.utilities import find_free_port


def _get_remote_env(
    runner: Runner, context: str, namespace: str, pod_name: str,
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
            ssh.popen([
                "-R", "*:{}:127.0.0.1:{}".format(remote_port, local_port)
            ])
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
    if sys.stdout.isatty() and not args.no_fs:
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
            kubectl_version, kube_cluster_version, operation, args.method
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
            "Context: {}, namespace: {}, kubectl_command: {}\n".format(
                args.context, args.namespace, runner.kubectl_cmd
            )
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
            args.in_local_vm and args.method == "vpn-tcp"
            and args.new_deployment is None and args.swap_deployment is None
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
            version = runner.get_output(["ssh", "-V"],
                                        stdin=DEVNULL,
                                        stderr=STDOUT)
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
        if not args.no_fs:
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
