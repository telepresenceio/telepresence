# Copyright 2018 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import subprocess
import typing
from argparse import Namespace

from telepresence.connect import SSH
from telepresence.proxy import RemoteInfo
from telepresence.runner import Runner

from . import edgectl
from .container import run_docker_command
from .local import launch_inject, launch_teleproxy, launch_vpn

LaunchType = typing.Callable[[
    Runner,
    RemoteInfo,
    typing.Dict[str, str],  # env
    int,
    SSH,
    str,
    typing.Dict[str, str],  # pod info
], "subprocess.Popen[bytes]"]


def check_local_command(runner: Runner, command: str) -> None:
    if runner.depend([command]):
        raise runner.fail("{}: command not found".format(command))


def setup_inject(runner: Runner, args: Namespace) -> LaunchType:
    command = ["torsocks"] + (args.run or ["bash", "--norc"])
    check_local_command(runner, command[1])
    runner.require(["torsocks"], "Please install torsocks (v2.1 or later)")
    if runner.chatty:
        runner.show(
            "Starting proxy with method 'inject-tcp', which has the following "
            "limitations: Go programs, static binaries, suid programs, and "
            "custom DNS implementations are not supported. For a full list of "
            "method limitations see "
            "https://telepresence.io/reference/methods.html"
        )

    if args.also_proxy:
        runner.show(
            "Note: --also-proxy is not meaningful with -m inject-tcp. "
            "The inject-tcp method sends all network traffic to the cluster."
        )

    def launch(
        runner_, _remote_info, env, socks_port, _ssh, _mount_dir, _pod_info
    ):
        return launch_inject(runner_, command, socks_port, env)

    return launch


def setup_vpn(runner: Runner, args: Namespace) -> LaunchType:
    command = args.run or ["bash", "--norc"]
    check_local_command(runner, command[0])
    runner.require(["sshuttle-telepresence"],
                   "Part of the Telepresence package. Try reinstalling.")
    if runner.platform == "linux":
        # Need conntrack for sshuttle on Linux:
        runner.require(["conntrack", "iptables"],
                       "Required for the vpn-tcp method")
    if runner.platform == "darwin":
        runner.require(["pfctl"], "Required for the vpn-tcp method")
    if edgectl.is_running_quiet(runner):
        runner.show(
            "Edge Control Daemon is running. At present, the vpn-tcp method "
            "conflicts with Edge Control Daemon. Please quit the daemon using "
            "'edgectl quit' to use the vpn-tcp method. We will fix this soon!"
        )
        raise runner.fail("Error: Edge Control Daemon is running")
    runner.require_sudo()
    if runner.platform == "linux":
        # Do a quick iptables sanity check, post sudo
        try:
            ipt_command = ["sudo", "iptables", "--list"]
            runner.get_output(ipt_command, stderr_to_stdout=True)
        except subprocess.CalledProcessError as exc:
            runner.show("Quick test of iptables failed:")
            print("\n> {}".format(" ".join(ipt_command)))
            print("{}\n".format(exc.output))
            runner.show(
                "The vpn-tcp method requires the use of iptables. "
                "If you're running Telepresence in a container, add network "
                "capabilities (docker run ... --cap-add=NET_ADMIN "
                "--cap-add=NET_BIND_SERVICE ...) or use a privileged "
                "container (docker run ... --privileged ...)."
            )
            runner.fail("Unable to use iptables")
    if runner.chatty:
        runner.show(
            "Starting proxy with method 'vpn-tcp', which has the following "
            "limitations: All processes are affected, only one telepresence "
            "can run per machine, and you can't use other VPNs. You may need "
            "to add cloud hosts and headless services with --also-proxy. For "
            "a full list of method limitations see "
            "https://telepresence.io/reference/methods.html"
        )

    def launch(
        runner_, remote_info, env, _socks_port, ssh, _mount_dir, _pod_info
    ):
        return launch_vpn(
            runner_, remote_info, command, args.also_proxy, env, ssh
        )

    return launch


def setup_teleproxy(runner: Runner, args: Namespace) -> LaunchType:
    command = args.run or ["bash", "--norc"]
    check_local_command(runner, command[0])

    if args.also_proxy:
        raise runner.fail(
            "The teleproxy method does not (yet) support --also-proxy"
        )

    # Verify that the daemon is running and ready
    runner.require(["edgectl"], "See URL FIXME")
    status = edgectl.run(runner, "status")
    if not status:
        raise runner.fail("Error: Edge Control daemon not running.")
    if not edgectl.net_overrides_ok(status):
        runner.show(edgectl.NET_OVERRIDES_MESSAGE)
        raise runner.fail("Error: Network overrides not ready")

    # Make sure we're not connected to a different cluster
    if edgectl.is_connected(status):
        connected_to = edgectl.what_cluster(status)
        if connected_to != (runner.kubectl.context, runner.kubectl.server):
            runner.show(edgectl.WRONG_CLUSTER_MESSAGE)
            raise runner.fail("Error: Connected to another cluster")

    if runner.chatty:
        runner.show(
            "Starting proxy with the EXPERIMENTAL method 'teleproxy', which "
            "has the following limitations: All processes are affected and "
            "you can't use other VPNs. Adding cloud hosts and headless "
            "services with --also-proxy is NOT supported (yet). For a full "
            "list of method limitations see "
            "https://telepresence.io/reference/methods.html"
        )

    def launch(
        runner_, remote_info, env, _socks_port, ssh, _mount_dir, _pod_info
    ):
        return launch_teleproxy(
            runner_, remote_info, command, args.also_proxy, env, ssh
        )

    return launch


def setup_container(runner: Runner, args: Namespace) -> LaunchType:
    runner.require_docker()
    if args.also_proxy:
        runner.show(
            "Note: --also-proxy is no longer required with --docker-run. "
            "The container method sends all network traffic to the cluster."
        )

    # Check for non-local docker
    local_docker_message = (
        "Telepresence's container method requires using a local Docker daemon."
        " Connecting to a remote daemon or a daemon running in a VM does not"
        " work at this time. If you are using Minikube's Docker daemon, launch"
        " Telepresence in a separate shell that does not have the Minikube"
        " Docker environment variables set."
    )
    try:
        id_in_container = runner.get_output(
            runner.docker(
                "run", "--rm", "-v", "{}:/tel".format(runner.temp),
                "alpine:3.6", "cat", "/tel/session_id.txt"
            ),
            timeout=30,
            reveal=True,
        ).strip()
        if id_in_container != runner.session_id:
            runner.write("Expected: [{}]".format(runner.session_id))
            runner.write("Got:      [{}]".format(id_in_container))
            runner.show("ID mismatch on local Docker check.")
            runner.show("\n" + local_docker_message)
            raise runner.fail("Error: Local Docker daemon required")
    except subprocess.CalledProcessError as exc:
        runner.show("Local Docker check failed: {}".format(exc.output))
        runner.show("\n" + local_docker_message)
        raise runner.fail("Error: Local Docker daemon required")

    def launch(
        runner_, remote_info, env, _socks_port, ssh, mount_dir, pod_info
    ):
        return run_docker_command(
            runner_,
            remote_info,
            args.docker_run,
            args.expose,
            args.to_pod,
            args.from_pod,
            args.container_to_host,
            env,
            ssh,
            mount_dir,
            args.docker_mount is not None,
            pod_info,
        )

    return launch


def setup(runner: Runner, args: Namespace) -> LaunchType:
    if args.method == "inject-tcp":
        return setup_inject(runner, args)

    if args.method == "vpn-tcp":
        return setup_vpn(runner, args)

    if args.method == "container":
        return setup_container(runner, args)

    if args.method == "teleproxy":
        return setup_teleproxy(runner, args)

    assert False, args.method
