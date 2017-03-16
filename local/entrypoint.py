#!/usr/bin/python3
"""
THIS IS A PROTOTYPE.

As a result the code is quite awful. Next up is rewriting it with tests and
abstractions.
"""

from json import loads
from os import setuid, environ, rename
from subprocess import Popen, check_output, check_call, CalledProcessError
from sys import argv, exit
import time


def _get_service_keys(environment):
    # XXX duplicated in remote-telepresence
    # XXX also check for TCPness.
    # Order matters for service_keys, need it to be consistent with port
    # forwarding order in remote container.
    result = [key for key in environment if key.endswith("_SERVICE_HOST")]
    result.sort(key=lambda s: s[:-len("_SERVICE_HOST")])
    return result


def get_remote_env(pod_name):
    env = str(check_output(["kubectl", "exec", pod_name, "env"]), "utf-8")
    result = {}
    for line in env.splitlines():
        key, value = line.split("=", 1)
        result[key] = value
    return result


def get_env_variables(pod_name):
    """Generate environment variables that match kubernetes."""
    remote_env = get_remote_env(pod_name)
    filter_keys = set()
    result = {}
    # XXX we're recreating the port generation logic
    i = 0
    for i, service_key in enumerate(_get_service_keys(remote_env)):
        port = str(2000 + i)
        ip = "127.0.0.1"
        # XXX bad abstraction
        name = service_key[:-len("_SERVICE_HOST")]
        # XXX ugh
        filter_prefix = "{}_PORT_{}_TCP".format(
            name, remote_env[name + "_SERVICE_PORT"]
        )
        filter_keys |= set(
            [filter_prefix + s for s in ("", "_PROTO", "_PORT", "_ADDR")]
        )
        # XXX will be wrong for UDP
        full_address = "tcp://{}:{}".format(ip, port)
        result[name + "_SERVICE_HOST"] = ip
        result[name + "_SERVICE_PORT"] = port
        result[name + "_PORT"] = full_address
        port_name = name + "_PORT_" + port + "_TCP"
        result[port_name] = full_address
        # XXX will break for UDP
        result[port_name + "_PROTO"] = "tcp"
        result[port_name + "_PORT"] = port
        result[port_name + "_ADDR"] = ip
    for key, value in remote_env.items():
        # We don't want env variables that are service addresses (did those
        # above) nor those that are already present in this container. XXX
        # we're getting env variables from telepresence that are
        # image-specific, not coming from the Deployment. figure out way to
        # differentiate.
        if key not in result and key not in environ and key not in filter_keys:
            result[key] = value
    return result


def write_env(pod_name):
    with open("/output/out.env.tmp", "w") as f:
        for key, value in get_env_variables(pod_name).items():
            f.write("{}={}\n".format(key, value))
    rename("/output/out.env.tmp", "/output/out.env")


def write_etc_hosts(additional_hosts):
    """
    Update /etc/hosts with records that match k8s DNS entries for services.
    """
    services_json = loads(
        str(
            check_output(["kubectl", "get", "service", "-o", "json"]), "utf-8"
        )
    )
    with open("/etc/hosts", "a") as hosts:
        for service in services_json["items"]:
            name = service["metadata"]["name"]
            namespace = service["metadata"]["namespace"]
            hosts.write("127.0.0.1 {}\n".format(name))
            hosts.write(
                "127.0.0.1 {}.{}.svc.cluster.local\n".format(name, namespace)
            )
        for host in additional_hosts:
            hosts.write("127.0.0.1 {}\n".format(host))


def get_pod_name(deployment_name):
    """Given the deployment name, return the name of its pod."""
    expected_metadata = loads(
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
    )["spec"]["template"]["metadata"]
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
        if not set(expected_metadata.get("labels", {}).items()).issubset(
            set(pod["metadata"].get("labels", {}).items())
        ):
            print("Labels don't match.")
            continue
        if (name.startswith(deployment_name + "-")
            and
            pod["metadata"]["namespace"] == expected_metadata.get(
                "namespace", "default")
            and
            phase in (
                "Pending", "Running"
        )):
            print("Looks like we've found our pod!")
            return name
    raise RuntimeError(
        "Telepresence pod not found for Deployment '{}'.".
        format(deployment_name)
    )


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


def wait_for_pod(pod_name):
    for i in range(30):
        phase = str(
            check_output([
                "kubectl", "get", "pod", pod_name, "-o",
                "jsonpath={.status.phase}"
            ]), "utf-8"
        ).strip()
        if phase == "Running":
            return
        time.sleep(1)
    raise RuntimeError("Pod isn't starting: {}".format(phase))


def main(uid, deployment_name, local_exposed_ports, custom_proxied_hosts):
    processes = []
    pod_name = get_pod_name(deployment_name)
    # Wait for pod to be running:
    wait_for_pod(pod_name)
    proxied_ports = set(range(2000, 2020)) | set(map(int, local_exposed_ports))
    proxied_ports.add(22)
    custom_ports = [int(s.split(":", 1)[1]) for s in custom_proxied_hosts]
    for port in custom_ports:
        if port in proxied_ports:
            exit((
                "OOPS: Can't proxy port {} more than once. "
                "Currently mapped ports: {}.This error is due "
                "to a limitation in Telepresence, see "
                "https://github.com/datawire/telepresence/issues/6"
            ).format(port, proxied_ports))
        else:
            proxied_ports.add(int(port))

    # 1. write /etc/hosts
    write_etc_hosts([s.split(":", 1)[0] for s in custom_proxied_hosts])
    # 2. forward remote port to here, by tunneling via remote SSH server:
    processes.append(Popen(["kubectl", "port-forward", pod_name, "22"]))
    wait_for_ssh()

    for port_number in local_exposed_ports:
        processes.append(
            Popen([
                "sshpass", "-phello", "ssh", "-q",
                "-oStrictHostKeyChecking=no", "root@localhost", "-R",
                "*:{}:127.0.0.1:{}".format(port_number, port_number), "-N"
            ])
        )

    # 3. start proxies for custom-mapped hosts:
    for host, port in [s.split(":", 1) for s in custom_proxied_hosts]:
        processes.append(
            Popen([
                "sshpass", "-phello", "ssh", "-q",
                "-oStrictHostKeyChecking=no", "root@localhost", "-L",
                "{}:{}:{}".format(port, host, port), "-N"
            ])
        )
    # 4. start proxies for Services:
    # XXX maybe just do everything via SSH, now that we have it?
    for port in range(2000, 2020):
        # XXX what if there is more than 20 services
        p = Popen(["kubectl", "port-forward", pod_name, str(port)])
        processes.append(p)
    time.sleep(5)
    # 5. write docker envfile, which tells CLI we're ready:
    setuid(uid)
    write_env(pod_name)
    for p in processes:
        p.wait()


if __name__ == '__main__':
    main(
        int(argv[1]), argv[2], argv[3].split(",")
        if argv[3] else [], argv[4].split(",") if argv[4] else []
    )
