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
from itertools import chain
from subprocess import CalledProcessError
from typing import List

from telepresence.proxy import RemoteInfo
from telepresence.runner import Runner
from telepresence.utilities import random_name


def _is_subnet_of(a: ipaddress.IPv4Network, b: ipaddress.IPv4Network) -> bool:
    try:
        return (
            b.network_address <= a.network_address
            and b.broadcast_address >= a.broadcast_address
        )
    except AttributeError:
        raise TypeError(
            "Unable to test subnet containment " +
            "between {} and {}".format(a, b)
        )


def covering_cidrs(ips: List[str]) -> List[str]:
    """
    Given list of IPs, return a list of CIDRs that covers them all.

    IPs that belong to a private network are presumed as a /24 and won't be
    expanded into a global network. IPs that belong to a global network are
    presumed as a /31.
    """
    def collapse(ns):
        return list(ipaddress.collapse_addresses(ns))

    assert len(ips) > 0
    ip_addresses = map(ipaddress.IPv4Address, ips)
    networks = collapse([
        ipaddress.IPv4Interface(str(ip) +
                                ("/31" if ip.is_global else "/24")).network
        for ip in ip_addresses
    ])
    # Increase network size until it combines everything:
    results = []
    while len(networks) > 1:
        network = networks[0]
        rest = networks[1:]
        supernet = network.supernet()

        while not supernet.is_global:
            collapsed_networks = collapse([supernet] + rest)
            # Keep the supernet if it did collapse networks together.
            if len(collapsed_networks) <= len(rest):
                network = supernet
                rest = [n for n in rest if not _is_subnet_of(n, network)]

            supernet = supernet.supernet()

        networks = rest
        results.append(network)

    return [n.with_prefixlen for n in chain(results, networks)]


# Script to dump resolved IPs to stdout as JSON list:

_GET_IPS_PY = """
import socket, sys, json

result = []
for host in sys.argv[1:]:
    host_ips = []
    for x in socket.getaddrinfo(host, None):
         if x[:2] == (socket.AF_INET, socket.SOCK_STREAM):
            host_ips.append(x[4][0])
    result.append(host_ips)
sys.stdout.write(json.dumps(result))
sys.stdout.flush()
"""


def get_proxy_cidrs(
    runner: Runner,
    remote_info: RemoteInfo,
    hosts_or_ips: List[str] = []
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
    result.update(
        context_cache.lookup("serviceCIDRs", lambda: serviceCIDR(runner))
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

    ipcache = runner.cache.child(runner.kubectl.context).child("ip-list")

    for proxy_target in hosts_or_ips:
        try:
            addr = ipaddress.ip_network(proxy_target)
        except ValueError:
            pass
        else:
            ip_ranges.append(str(addr))
            continue

        if proxy_target in ipcache:
            ip_ranges += ipcache[proxy_target]
            continue

        hostnames.append(proxy_target)

    if hostnames:
        try:
            hostname_ips = json.loads(
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
        hostname_ips = []

    resolved_ips = []  # type: List[str]
    for host, ips in zip(hostnames, hostname_ips):
        ipcache[host] = ips
        resolved_ips += ips

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
    else:
        for node in nodes:
            pod_cidr = node["spec"].get("podCIDR")
            if pod_cidr is not None:
                cidrs.add(pod_cidr)

    if len(cidrs) == 0:
        # Fallback to using pod IPs:
        try:
            pod_data = runner.get_output(
                runner.kubectl(
                    "get", "pods", "--all-namespaces", "-o", "json"
                )
            )
        except CalledProcessError as e:
            runner.write("Failed to get pods for all namespaces: {}".format(e))
            pod_data = runner.get_output(
                runner.kubectl("get", "pods", "-o", "json")
            )
        pods = json.loads(pod_data)["items"]
        pod_ips = []
        for pod in pods:
            try:
                pod_ips.append(pod["status"]["podIP"])
            except KeyError:
                # Apparently a problem on OpenShift
                pass
        if pod_ips:
            cidrs.update(covering_cidrs(pod_ips))

    return list(cidrs)


def serviceCIDR(runner: Runner) -> List[str]:
    """
    Get cluster service IP range.
    """
    serviceCIDR = cluster_serviceCIDR(runner)
    if not serviceCIDR:
        return guess_serviceCIDR(runner)
    return serviceCIDR


def cluster_serviceCIDR(runner: Runner) -> List[str]:
    """
    Get cluster service IP range from apiserver.
    """
    pods = json.loads(
        runner.get_output(
            runner.kubectl("get", "pods", "-n", "kube-system", "-o", "json")
        )
    )["items"]
    for pod_data in pods:
        for container in pod_data["spec"]["containers"]:
            if container["name"] != "kube-apiserver":
                continue

            for param in container["command"]:
                if param.startswith("--service-cluster-ip-range="):
                    return [param.split("=", 1)[1]]
            return []
    return []


def guess_serviceCIDR(runner: Runner) -> List[str]:
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
    service_cidrs = covering_cidrs(service_ips)
    # Delete new services:
    for new_service in new_services:
        runner.check_call(runner.kubectl("delete", "service", new_service))

    if runner.chatty:
        runner.show(
            "Guessing that Services IP range is {}. Services started after"
            " this point will be inaccessible if are outside this range;"
            " restart telepresence if you can't access a "
            "new Service.\n".format(service_cidrs)
        )

    return service_cidrs
