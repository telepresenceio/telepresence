#!/usr/bin/python3
"""
THIS IS A PROTOTYPE.

As a result the code is quite awful. Next up is rewriting it with tests and
abstractions.
"""

from json import loads
from os import chown, rename
from subprocess import (
    Popen,
    check_output,
    check_call,
    CalledProcessError,
    TimeoutExpired,
)
from sys import argv
import time


class RemoteInfo(object):
    """
    Information about the remote setup.

    :ivar deployment_name str: The name of the Deployment object.
    :ivar pod_name str: The name of the pod created by the Deployment.
    :ivar deployment_config dict: The decoded k8s object (i.e. JSON/YAML).
    :ivar container_config dict: The container within the Deployment JSON.
    :ivar container_name str: The name of the container.
    :ivar pod_environment dict: Environment variables in the remote
        Telepresence pod.
    """

    def __init__(self, deployment_name, pod_name, deployment_config):
        self.deployment_name = deployment_name
        self.pod_name = pod_name
        self.deployment_config = deployment_config
        cs = deployment_config["spec"]["template"]["spec"]["containers"]
        self.container_config = [
            c for c in cs if "telepresence-k8s" in c["image"]
        ][0]
        self.container_name = self.container_config["name"]
        self.pod_environment = _get_remote_env(pod_name, self.container_name)
        self.service_names = _get_service_names(self.pod_environment)


def _get_service_names(environment):
    """Return names of Services, as used in env variable names."""
    # XXX need to check for TCPness.
    # Order matters for service_keys, need it to be consistent with port
    # forwarding order in remote container.
    result = [
        key[:-len("_SERVICE_HOST")] for key in environment
        if key.endswith("_SERVICE_HOST")
    ]
    result.sort()
    return result


def _get_remote_env(pod_name, container_name):
    """Get the environment variables in the remote pod."""
    env = str(
        check_output([
            "kubectl", "exec", pod_name, "--container", container_name, "env"
        ]), "utf-8"
    )
    result = {}
    for line in env.splitlines():
        key, value = line.split("=", 1)
        result[key] = value
    return result


def get_deployment_set_keys(remote_info):
    """Get the set of environment variables names set by the Deployment."""
    return set(
        [var["name"] for var in remote_info.container_config.get("env", [])]
    )


def get_env_variables(remote_info):
    """
    Generate environment variables that match kubernetes.
    """
    remote_env = remote_info.pod_environment
    deployment_set_keys = get_deployment_set_keys(remote_info)
    service_names = remote_info.service_names
    # Tell local process about the remote setup, useful for testing and
    # debugging:
    socks_result = {
        "TELEPRESENCE_POD": remote_info.pod_name,
        "TELEPRESENCE_CONTAINER": remote_info.container_name
    }
    # ips proxied via socks, can copy addresses unmodified:
    for key, value in remote_env.items():
        if key in deployment_set_keys:
            # Copy over Deployment-set env variables:
            socks_result[key] = value
        for service_name in service_names:
            # Copy over Service env variables to SOCKS variant:
            if key.startswith(service_name + "_") and (
                key.endswith("_ADDR") or key.endswith("_PORT") or
                key.endswith("_PROTO") or key.endswith("_HOST") or
                key.endswith("_TCP")
            ):
                socks_result[key] = value
    return socks_result


def write_env(remote_info, uid):
    for_local_env = get_env_variables(remote_info)
    with open("/output/unproxied.env.tmp", "w") as f:
        for key, value in for_local_env.items():
            f.write("{}={}\n".format(key, value))
    chown("/output/unproxied.env.tmp", uid, uid)
    rename("/output/unproxied.env.tmp", "/output/unproxied.env")


def get_remote_info(deployment_name):
    """Given the deployment name, return a RemoteInfo object."""
    deployment = loads(
        str(
            check_output([
                "kubectl",
                "get",
                "deployment",
                "-o",
                "json",
                deployment_name,
                "--export",
            ]), "utf-8"
        )
    )
    expected_metadata = deployment["spec"]["template"]["metadata"]
    print("Expected metadata for pods: {}".format(expected_metadata))
    pods = loads(
        str(
            check_output(["kubectl", "get", "pod", "-o", "json", "--export"]),
            "utf-8"
        )
    )["items"]

    for pod in pods:
        name = pod["metadata"]["name"]
        phase = pod["status"]["phase"]
        print(
            "Checking {} (phase {})...".
            format(pod["metadata"].get("labels"), phase)
        )
        if not set(expected_metadata.get("labels", {}).items()
                   ).issubset(set(pod["metadata"].get("labels", {}).items())):
            print("Labels don't match.")
            continue
        if (name.startswith(deployment_name + "-")
            and
            pod["metadata"]["namespace"] == deployment["metadata"].get(
                "namespace", "default")
            and
            phase in (
                "Pending", "Running"
        )):
            print("Looks like we've found our pod!")
            return RemoteInfo(deployment_name, name, deployment)

    raise RuntimeError(
        "Telepresence pod not found for Deployment '{}'.".
        format(deployment_name)
    )


def ssh(args):
    """Connect to remote pod via SSH.

    Returns Popen object.
    """
    return Popen([
        # Password is hello (see remote/Dockerfile):
        "sshpass",
        "-phello",
        "ssh",
        # SSH with no warnings:
        "-q",
        # Don't validate host key:
        "-oStrictHostKeyChecking=no",
        # Ping once a second; after three retries will disconnect:
        "-oServerAliveInterval=1",
        # No shell:
        "-N",
        "root@localhost",
    ] + args)


def wait_for_ssh():
    for i in range(30):
        try:
            check_call([
                "sshpass", "-phello", "ssh", "-q",
                "-oStrictHostKeyChecking=no", "root@localhost", "/bin/true"
            ])
        except CalledProcessError:
            time.sleep(1)
        else:
            return
    raise RuntimeError("SSH isn't starting.")


def wait_for_pod(remote_info):
    for i in range(120):
        try:
            pod = loads(
                str(
                    check_output([
                        "kubectl", "get", "pod", remote_info.pod_name, "-o",
                        "json"
                    ]), "utf-8"
                )
            )
        except CalledProcessError:
            time.sleep(1)
            continue
        if pod["status"]["phase"] == "Running":
            for container in pod["status"]["containerStatuses"]:
                if container["name"] == remote_info.container_name and (
                    container["ready"]
                ):
                    return
        time.sleep(1)
    raise RuntimeError(
        "Pod isn't starting or can't be found: {}".format(pod["status"])
    )


SOCKS_PORT = 9050


def connect(
    remote_info,
    local_exposed_ports,
    expose_host,
):
    """
    Start all the processes that handle remote proxying.

    Return list of Popen instances.
    """
    processes = []
    # forward remote port to here, by tunneling via remote SSH server:
    processes.append(
        Popen(["kubectl", "port-forward", remote_info.pod_name, "22"])
    )
    wait_for_ssh()

    for port_number in local_exposed_ports:
        # XXX really only need to bind to external port...
        processes.append(
            ssh([
                "-R",
                "*:{}:{}:{}".format(port_number, expose_host, port_number)
            ])
        )

    # start tunnel to remote SOCKS proxy, for telepresence --run.
    # XXX really only need to bind to external port...
    processes.append(
        ssh(["-L", "*:{}:127.0.0.1:{}".format(SOCKS_PORT, SOCKS_PORT)])
    )

    return processes


def killall(processes):
    for p in processes:
        if p.poll() is None:
            p.terminate()
    for p in processes:
        try:
            p.wait(timeout=1)
        except TimeoutExpired:
            p.kill()
            p.wait()


def main(uid, deployment_name, local_exposed_ports, expose_host):
    remote_info = get_remote_info(deployment_name)

    # Wait for pod to be running:
    wait_for_pod(remote_info)

    processes = connect(
        remote_info,
        local_exposed_ports,
        expose_host,
    )

    # write docker envfile, which tells CLI we're ready:
    time.sleep(5)
    write_env(remote_info, uid)

    # Now, poll processes; if one dies kill them all and restart them:
    while True:
        for p in processes:
            code = p.poll()
            if code is not None:
                print("A subprocess died, killing all processes...")
                killall(processes)
                # Unfortunatly torsocks doesn't deal well with connections
                # being lost, so best we can do is shut down.
                raise SystemExit(3)


if __name__ == '__main__':
    main(int(argv[1]), argv[2], argv[3].split(",") if argv[3] else [], argv[4])
