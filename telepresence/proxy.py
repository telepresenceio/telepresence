import argparse
import atexit
import re
import sys
from shutil import which
from subprocess import CalledProcessError
from time import time, sleep
from typing import Tuple, Dict

from telepresence.cleanup import Subprocesses
from telepresence.container import MAC_LOOPBACK_IP
from telepresence.deployment import create_new_deployment, \
    swap_deployment_openshift, supplant_deployment
from telepresence.expose import expose_local_services
from telepresence.remote import RemoteInfo, get_remote_info
from telepresence.remote_env import get_env_variables
from telepresence.runner import Runner
from telepresence.ssh import SSH
from telepresence.utilities import find_free_port


def connect(
    runner: Runner, remote_info: RemoteInfo, cmdline_args: argparse.Namespace
) -> Tuple[Subprocesses, int, SSH]:
    """
    Start all the processes that handle remote proxying.

    Return (Subprocesses, local port of SOCKS proxying tunnel, SSH instance).
    """
    span = runner.span()
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

    span.end()
    return processes, socks_port, ssh


def start_proxy(runner: Runner, args: argparse.Namespace
                ) -> Tuple[Subprocesses, Dict[str, str], int, SSH, RemoteInfo]:
    """Start the kubectl port-forward and SSH clients that do the proxying."""
    span = runner.span()
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
    if args.mount and sys.stdout.isatty():
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
            args.deployment, run_id, container_json = supplant_deployment(
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

    span.end()
    return processes, env, socks_port, ssh, remote_info
