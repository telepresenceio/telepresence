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

import ipaddress
import json
from subprocess import CalledProcessError
from typing import List

from telepresence.connect import SSH
from telepresence.proxy import RemoteInfo
from telepresence.runner import Runner
from telepresence.utilities import random_name

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
    runner: Runner, remote_info: RemoteInfo, hosts_or_ips: List[str]
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

    span = runner.span()

    # Run script to convert --also-proxy hostnames to IPs, doing name
    # resolution inside Kubernetes, so we get cloud-local IP addresses for
    # cloud resources:
    result = set(k8s_resolve(runner, remote_info, hosts_or_ips))
    context_cache = runner.cache.child(runner.kubectl.context)
    result.update(context_cache.lookup("podCIDRs", lambda: podCIDRs(runner)))
    result.add(
        context_cache.lookup("serviceCIDR", lambda: serviceCIDR(runner))
    )

    span.end()
    return list(result)


def k8s_resolve(
    runner: Runner, remote_info: RemoteInfo, hosts_or_ips: List[str]
) -> List[str]:
    """
    Resolve a list of host and/or ip addresses inside the cluster
    using the context, namespace, and remote_info supplied. Note that
    if any hostname fails to resolve this will fail Telepresence.
    """
    # Separate hostnames from IPs and IP ranges
    hostnames = []
    ip_ranges = []

    ipcache = runner.cache.child(runner.kubectl.context).child("ips")

    for proxy_target in hosts_or_ips:
        try:
            addr = ipaddress.ip_network(proxy_target)
        except ValueError:
            pass
        else:
            ip_ranges.append(str(addr))
            continue

        if proxy_target in ipcache:
            ip_ranges.append(ipcache[proxy_target])
            continue

        hostnames.append(proxy_target)

    if hostnames:
        try:
            resolved_ips = json.loads(
                runner.get_output(
                    runner.kubectl(
                        "exec", "--container=" + remote_info.container_name,
                        remote_info.pod_name, "--", "python3", "-c",
                        _GET_IPS_PY, *hostnames
                    )
                )
            )
        except CalledProcessError as e:
            runner.write(str(e))
            raise runner.fail(
                "We failed to do a DNS lookup inside Kubernetes for the "
                "hostname(s) you listed in "
                "--also-proxy ({}). Maybe you mistyped one of them?".format(
                    ", ".join(hosts_or_ips)
                )
            )
    else:
        resolved_ips = []

    for host, ip in zip(hostnames, resolved_ips):
        ipcache[host] = ip

    return resolved_ips + ip_ranges


def podCIDRs(runner: Runner):
    """
    Get pod IPs from nodes if possible, otherwise use pod IPs as heuristic:
    """
    cidrs = set()
    try:
        nodes = json.loads(
            runner.get_output(runner.kubectl("get", "nodes", "-o", "json"))
        )["items"]
    except CalledProcessError as e:
        runner.write("Failed to get nodes: {}".format(e))
        # Fallback to using pod IPs:
        pods = json.loads(
            runner.get_output(runner.kubectl("get", "pods", "-o", "json"))
        )["items"]
        pod_ips = []
        for pod in pods:
            try:
                pod_ips.append(pod["status"]["podIP"])
            except KeyError:
                # Apparently a problem on OpenShift
                pass
        if pod_ips:
            cidrs.add(covering_cidr(pod_ips))
    else:
        for node in nodes:
            pod_cidr = node["spec"].get("podCIDR")
            if pod_cidr is not None:
                cidrs.add(pod_cidr)
    return list(cidrs)


def serviceCIDR(runner: Runner):
    """
    Get service IP range, based on heuristic of constructing CIDR from
    existing Service IPs. We create more services if there are less
    than 8, to ensure some coverage of the IP range.
    """

    def get_service_ips():
        services = json.loads(
            runner.get_output(runner.kubectl("get", "services", "-o", "json"))
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
        runner.check_call(
            runner.kubectl(
                "create", "service", "clusterip", new_service, "--tcp=3000"
            )
        )
        new_services.append(new_service)
    if new_services:
        service_ips = get_service_ips()
    # Store Service CIDR:
    service_cidr = covering_cidr(service_ips)
    # Delete new services:
    for new_service in new_services:
        runner.check_call(runner.kubectl("delete", "service", new_service))

    if runner.chatty:
        runner.show(
            "Guessing that Services IP range is {}. Services started after"
            " this point will be inaccessible if are outside this range;"
            " restart telepresence if you can't access a "
            "new Service.\n".format(service_cidr)
        )
    return service_cidr


def connect_sshuttle(
    runner: Runner, remote_info: RemoteInfo, hosts_or_ips: List[str], ssh: SSH
):
    """Connect to Kubernetes using sshuttle."""
    span = runner.span()
    sshuttle_method = "auto"
    if runner.platform == "linux":
        # sshuttle tproxy mode seems to have issues:
        sshuttle_method = "nat"
    runner.launch(
        "sshuttle",
        [
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
        ] + get_proxy_cidrs(runner, remote_info, hosts_or_ips),
        keep_session=True,  # Avoid trouble with interactive sudo
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

    subspan = runner.span("sshuttle-wait")
    probes = 0
    for _ in runner.loop_until(20, 0.1):
        probes += 1
        try:
            get_hellotelepresence()
            if probes >= REQUIRED_HELLOTELEPRESENCE_DNS_PROBES:
                break
        except CalledProcessError:
            pass

    get_hellotelepresence()
    subspan.end()
    span.end()
