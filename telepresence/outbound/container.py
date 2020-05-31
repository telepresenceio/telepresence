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

import argparse
import json
import os
import os.path
import subprocess
from typing import Callable, Dict, List, Optional, Tuple

from telepresence import TELEPRESENCE_LOCAL_IMAGE
from telepresence.cli import PortMapping
from telepresence.connect import SSH
from telepresence.outbound.cidr import get_proxy_cidrs
from telepresence.proxy import RemoteInfo
from telepresence.runner import Runner
from telepresence.utilities import find_free_port, random_name


def make_docker_kill(runner: Runner, name: str) -> Callable[[], None]:
    """Return a function that will kill a named docker container."""
    def kill() -> None:
        runner.check_call(runner.docker("stop", "--time=1", name))

    return kill


def parse_docker_args(docker_run: List[str]) -> Tuple[List[str], List[str]]:
    """Separate --publish flags from the rest of the docker arguments"""
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--publish", "-p", action="append", default=[])
    publish_ns, docker_args = parser.parse_known_args(docker_run)
    publish_args = ["-p={}".format(pub) for pub in publish_ns.publish]
    return docker_args, publish_args


def parse_resolv_conf(contents: str) -> List[str]:
    """
    Try to extract nameserver, search path, and ndots info from the pod's
    resolv.conf file.
    """
    res = []
    for line in contents.splitlines():
        line = line.strip()
        if not line:
            continue
        tokens = line.split()
        keyword = tokens[0].lower()
        args = tokens[1:]

        if keyword == "nameserver":
            res.append("--dns={}".format(args[0]))
        elif keyword == "search":
            for arg in args:
                res.append("--dns-search={}".format(arg))
        elif keyword == "options":
            for arg in args:
                res.append("--dns-opt={}".format(arg))
        else:
            pass  # Ignore the rest
    return res


def parse_hosts_aliases(contents: str) -> List[str]:
    """
    Try to extract IP, and corresponding host names from hosts file for each
    hostAlias, and create the corresponding --add-host docker run argument.
    """
    res = []

    host_alias = False

    for line in contents.splitlines():

        line = line.strip()

        if not line:
            continue

        if line.startswith("#"):
            host_alias = line.__contains__("HostAliases")
            continue

        if host_alias:
            tokens = line.split()
            ip = tokens[0]
            hosts = tokens[1:]

            for host in hosts:
                res.append("--add-host={}:{}".format(host, ip))

    return res


def run_docker_command(
    runner: Runner,
    remote_info: RemoteInfo,
    docker_run: List[str],
    expose: PortMapping,
    to_pod: List[int],
    from_pod: List[int],
    container_to_host: PortMapping,
    remote_env: Dict[str, str],
    ssh: SSH,
    mount_dir: Optional[str],
    use_docker_mount: Optional[bool],
    pod_info: Dict[str, str],
) -> "subprocess.Popen[bytes]":
    """
    --docker-run support.

    Connect using sshuttle running in a Docker container, and then run user
    container.

    :param remote_env: Dictionary with environment on remote pod.
    :param mount_dir: Path to local directory where remote pod's filesystem is
        mounted.
    """
    # Update environment:
    remote_env["TELEPRESENCE_METHOD"] = "container"  # mostly just for tests :(

    # Extract --publish flags and add them to the sshuttle container, which is
    # responsible for defining the network entirely.
    docker_args, publish_args = parse_docker_args(docker_run)

    # Point a host port to the network container's sshd
    container_sshd_port = find_free_port()
    publish_args.append(
        "--publish=127.0.0.1:{}:38022/tcp".format(container_sshd_port)
    )
    local_ssh = SSH(runner, container_sshd_port, "root@127.0.0.1")

    # Start the network (sshuttle) container:
    name = random_name()
    config = {
        "cidrs": get_proxy_cidrs(runner, remote_info),
        "expose_ports": list(expose.local_to_remote()),
        "to_pod": to_pod,
        "from_pod": from_pod,
    }
    dns_args = []
    if "hostname" in pod_info:
        dns_args.append("--hostname={}".format(pod_info["hostname"].strip()))
    if "hosts" in pod_info:
        dns_args.extend(parse_hosts_aliases(pod_info["hosts"]))
    if "resolv" in pod_info:
        dns_args.extend(parse_resolv_conf(pod_info["resolv"]))

    # Image already has tini init so doesn't need --init option:
    span = runner.span()
    runner.launch(
        "Network container",
        runner.docker(
            "run", *publish_args, *dns_args, "--rm", "--privileged",
            "--name=" + name, TELEPRESENCE_LOCAL_IMAGE, "proxy",
            json.dumps(config)
        ),
        killer=make_docker_kill(runner, name),
        keep_session=runner.sudo_for_docker,
    )

    # Set up ssh tunnel to allow the container to reach the cluster
    if not local_ssh.wait():
        raise RuntimeError("SSH to the network container failed to start.")

    container_forward_args = ["-R", "38023:127.0.0.1:{}".format(ssh.port)]
    for container_port, host_port in container_to_host.local_to_remote():
        if runner.chatty:
            runner.show(
                "Forwarding container port {} to host port {}.".format(
                    container_port, host_port
                )
            )
        container_forward_args.extend([
            "-R", "{}:127.0.0.1:{}".format(container_port, host_port)
        ])
    runner.launch(
        "Local SSH port forward", local_ssh.bg_command(container_forward_args)
    )

    # Wait for sshuttle to be running:
    sshuttle_ok = False
    for _ in runner.loop_until(120, 1):
        try:
            runner.check_call(
                runner.docker(
                    "run", "--network=container:" + name, "--rm",
                    TELEPRESENCE_LOCAL_IMAGE, "wait"
                )
            )
        except subprocess.CalledProcessError as e:
            if e.returncode == 100:
                # We're good!
                sshuttle_ok = True
                break
            elif e.returncode == 125:
                # Docker failure, probably due to original container not
                # starting yet... so try again:
                continue
            else:
                raise
        else:
            raise RuntimeError(
                "Waiting container exited prematurely. File a bug, please!"
            )
    if not sshuttle_ok:
        # This used to loop forever. Now we time out after two minutes.
        raise RuntimeError(
            "Waiting for network container timed out. File a bug, please!"
        )

    # Start the container specified by the user:
    container_name = random_name()
    docker_command = runner.docker(
        "run",
        "--name=" + container_name,
        "--network=container:" + name,
        env=True,
    )

    # Prepare container environment
    for key in remote_env:
        docker_command.append("-e={}".format(key))
    docker_env = os.environ.copy()
    docker_env.update(remote_env)

    if mount_dir:
        if use_docker_mount:
            mount_volume = "telepresence-" + runner.session_id
        else:
            mount_volume = mount_dir

        docker_command.append("--volume={}:{}".format(mount_volume, mount_dir))

    # Don't add --init if the user is doing something with it
    init_args = [
        arg for arg in docker_args
        if arg == "--init" or arg.startswith("--init=")
    ]
    # Older versions of Docker don't have --init:
    docker_run_help = runner.get_output(["docker", "run", "--help"])
    if not init_args and "--init" in docker_run_help:
        docker_command += ["--init"]
    docker_command += docker_args
    span.end()

    runner.show("Setup complete. Launching your container.")
    process = subprocess.Popen(docker_command, env=docker_env)

    def terminate_if_alive() -> None:
        runner.write("Shutting down containers...\n")
        if process.poll() is None:
            runner.write("Killing local container...\n")
            make_docker_kill(runner, container_name)()

    runner.add_cleanup("Terminate local container", terminate_if_alive)
    return process
