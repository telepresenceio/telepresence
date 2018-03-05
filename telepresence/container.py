import argparse
import atexit
import json
import sys
from subprocess import CalledProcessError, Popen
from time import sleep
from typing import List, Callable, Dict, Tuple

import os
import os.path
from tempfile import NamedTemporaryFile

from telepresence import TELEPRESENCE_LOCAL_IMAGE
from telepresence.cleanup import Subprocesses, wait_for_exit
from telepresence.remote import RemoteInfo, mount_remote_volumes
from telepresence.runner import Runner
from telepresence.ssh import SSH
from telepresence.utilities import random_name
from telepresence.vpn import get_proxy_cidrs

# IP that shouldn't be in use on Internet, *or* local networks:
MAC_LOOPBACK_IP = "198.18.0.254"

# Whether Docker requires sudo
SUDO_FOR_DOCKER = os.path.exists("/var/run/docker.sock") and not os.access(
    "/var/run/docker.sock", os.W_OK
)


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


def parse_docker_args(docker_run: List[str]) -> Tuple[List[str], List[str]]:
    """Separate --publish flags from the rest of the docker arguments"""
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--publish", "-p", action="append", default=[])
    publish_ns, docker_args = parser.parse_known_args(docker_run)
    publish_args = ["-p={}".format(pub) for pub in publish_ns.publish]
    return docker_args, publish_args


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

    # Extract --publish flags and add them to the sshuttle container, which is
    # responsible for defining the network entirely.
    docker_args, publish_args = parse_docker_args(args.docker_run)

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
            docker_runify(
                publish_args + [
                    "--rm", "--privileged", "--name=" + name,
                    TELEPRESENCE_LOCAL_IMAGE, "proxy",
                    json.dumps(config)
                ]
            )
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
    # Don't add --init if the user is doing something with it
    init_args = [
        arg for arg in docker_args
        if arg == "--init" or arg.startswith("--init=")
    ]
    # Older versions of Docker don't have --init:
    if not init_args and "--init" in runner.get_output([
        "docker", "run", "--help"
    ]):
        docker_command += ["--init"]
    docker_command += docker_args
    p = Popen(docker_command)

    def terminate_if_alive():
        runner.write("Shutting down containers...\n")
        if p.poll() is None:
            runner.write("Killing local container...\n")
            make_docker_kill(runner, container_name)()

        mount_cleanup()

    atexit.register(terminate_if_alive)
    wait_for_exit(runner, p, subprocesses)
