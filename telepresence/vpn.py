import argparse
import ipaddress
import json
import sys
from subprocess import CalledProcessError, Popen
from time import time, sleep
from typing import List, Dict

from telepresence.ssh import SSH
from telepresence.cleanup import Subprocesses
from telepresence.remote import RemoteInfo
from telepresence.utilities import random_name
from telepresence.runner import Runner


# The number of DNS probes which must be issued during startup before the
# sshuttle-proxied DNS system is considered properly "primed" with respect to
# search domains.
REQUIRED_HELLOTELEPRESENCE_DNS_PROBES = 10


def covering_cidr(ips: List[str]) -> str:
    """
    Given list of IPs, return CIDR that covers them all.

    Presumes it's at least a /24.
    """

    def collapse(ns):
        return list(ipaddress.collapse_addresses(ns))

    assert len(ips) > 0
    networks = collapse([
        ipaddress.IPv4Interface(ip + "/24").network for ip in ips
    ])
    # Increase network size until it combines everything:
    while len(networks) > 1:
        networks = collapse([networks[0].supernet()] + networks[1:])
    return networks[0].with_prefixlen


# Script to dump resolved IPs to stdout as JSON list:

_GET_IPS_PY = """
import socket, sys, json

result = []
for host in sys.argv[1:]:
    result.append(socket.gethostbyname(host))
sys.stdout.write(json.dumps(result))
sys.stdout.flush()
"""


def get_proxy_cidrs(
    runner: Runner, args: argparse.Namespace, remote_info: RemoteInfo,
    service_address: str
) -> List[str]:
    """
    Figure out which IP ranges to route via sshuttle.

    1. Given the IP address of a service, figure out IP ranges used by
       Kubernetes services.
    2. Extract pod ranges from API.
    3. Any hostnames/IPs given by the user using --also-proxy.

    See https://github.com/kubernetes/kubernetes/issues/25533 for eventual
    long-term solution for service CIDR.
    """

    # Run script to convert --also-proxy hostnames to IPs, doing name
    # resolution inside Kubernetes, so we get cloud-local IP addresses for
    # cloud resources:
    def resolve_ips():
        # Separate hostnames from IPs and IP ranges
        hostnames = []
        ip_ranges = []

        for proxy_target in args.also_proxy:
            try:
                addr = ipaddress.ip_network(proxy_target)
            except ValueError:
                pass
            else:
                ip_ranges.append(str(addr))
                continue

            hostnames.append(proxy_target)

        resolved_ips = json.loads(
            runner.get_kubectl(
                args.context, args.namespace, [
                    "exec", "--container=" + remote_info.container_name,
                    remote_info.pod_name, "--", "python3", "-c", _GET_IPS_PY
                ] + hostnames
            )
        )
        return resolved_ips + ip_ranges

    try:
        result = set(resolve_ips())
    except CalledProcessError as e:
        runner.write(str(e))
        raise SystemExit(
            "We failed to do a DNS lookup inside Kubernetes for the "
            "hostname(s) you listed in "
            "--also-proxy ({}). Maybe you mistyped one of them?".format(
                ", ".join(args.also_proxy)
            )
        )

    # Get pod IPs from nodes if possible, otherwise use pod IPs as heuristic:
    try:
        nodes = json.loads(
            runner.get_output([
                runner.kubectl_cmd, "get", "nodes", "-o", "json"
            ])
        )["items"]
    except CalledProcessError as e:
        runner.write("Failed to get nodes: {}".format(e))
        # Fallback to using pod IPs:
        pods = json.loads(
            runner.get_output([
                runner.kubectl_cmd, "get", "pods", "-o", "json"
            ])
        )["items"]
        pod_ips = []
        for pod in pods:
            try:
                pod_ips.append(pod["status"]["podIP"])
            except KeyError:
                # Apparently a problem on OpenShift
                pass
        if pod_ips:
            result.add(covering_cidr(pod_ips))
    else:
        for node in nodes:
            pod_cidr = node["spec"].get("podCIDR")
            if pod_cidr is not None:
                result.add(pod_cidr)

    # Add service IP range, based on heuristic of constructing CIDR from
    # existing Service IPs. We create more services if there are less than 8,
    # to ensure some coverage of the IP range:
    def get_service_ips():
        services = json.loads(
            runner.get_output([
                runner.kubectl_cmd, "get", "services", "-o", "json"
            ])
        )["items"]
        # FIXME: Add test(s) here so we don't crash on, e.g., ExternalName
        return [
            svc["spec"]["clusterIP"] for svc in services
            if svc["spec"].get("clusterIP", "None") != "None"
        ]

    service_ips = get_service_ips()
    new_services = []  # type: List[str]
    # Ensure we have at least 8 ClusterIP Services:
    while len(service_ips) + len(new_services) < 8:
        new_service = random_name()
        runner.check_call([
            runner.kubectl_cmd, "create", "service", "clusterip", new_service,
            "--tcp=3000"
        ])
        new_services.append(new_service)
    if new_services:
        service_ips = get_service_ips()
    # Store Service CIDR:
    service_cidr = covering_cidr(service_ips)
    result.add(service_cidr)
    # Delete new services:
    for new_service in new_services:
        runner.check_call([
            runner.kubectl_cmd, "delete", "service", new_service
        ])

    if sys.stderr.isatty():
        print(
            "Guessing that Services IP range is {}. Services started after"
            " this point will be inaccessible if are outside this range;"
            " restart telepresence if you can't access a "
            "new Service.\n".format(service_cidr),
            file=sys.stderr
        )

    return list(result)


def connect_sshuttle(
    runner: Runner, remote_info: RemoteInfo, args: argparse.Namespace,
    subprocesses: Subprocesses, env: Dict[str, str], ssh: SSH
):
    """Connect to Kubernetes using sshuttle."""
    # Make sure we have sudo credentials by doing a small sudo in advance
    # of sshuttle using it:
    Popen(["sudo", "true"]).wait()
    sshuttle_method = "auto"
    if sys.platform.startswith("linux"):
        # sshuttle tproxy mode seems to have issues:
        sshuttle_method = "nat"
    subprocesses.append(
        runner.popen([
            "sshuttle-telepresence",
            "-v",
            "--dns",
            "--method",
            sshuttle_method,
            "-e",
            (
                "ssh -oStrictHostKeyChecking=no " +
                "-oUserKnownHostsFile=/dev/null -F /dev/null"
            ),
            # DNS proxy running on remote pod:
            "--to-ns",
            "127.0.0.1:9053",
            "-r",
            "telepresence@localhost:" + str(ssh.port),
        ] + get_proxy_cidrs(
            runner, args, remote_info, env["KUBERNETES_SERVICE_HOST"]
        ))
    )

    # sshuttle will take a while to startup. We can detect it being up when
    # DNS resolution of services starts working. We use a specific single
    # segment so any search/domain statements in resolv.conf are applied,
    # which then allows the DNS proxy to detect the suffix domain and
    # filter it out.
    def get_hellotelepresence(counter=iter(range(10000))):
        # On Macs, and perhaps elsewhere, there is OS-level caching of
        # NXDOMAIN, so bypass caching by sending new domain each time. Another,
        # less robust alternative, is to `killall -HUP mDNSResponder`.
        runner.get_output([
            "python3", "-c",
            "import socket; socket.gethostbyname('hellotelepresence{}')".
            format(next(counter))
        ])

    start = time()
    probes = 0
    while time() - start < 20:
        probes += 1
        try:
            get_hellotelepresence()
            if probes >= REQUIRED_HELLOTELEPRESENCE_DNS_PROBES:
                break
        except CalledProcessError:
            sleep(0.1)

    sleep(1)  # just in case there's more to startup
    get_hellotelepresence()
