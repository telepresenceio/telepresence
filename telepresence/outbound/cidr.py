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
from typing import List, Optional

from telepresence.proxy import RemoteInfo
from telepresence.runner import Runner
from telepresence.utilities import random_name


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
    result.add(
        context_cache.lookup("serviceCIDR", lambda: serviceCIDR(runner))
    )

    span.end()
    return list(result)


def is_private_cidr(cidr: str) -> bool:
    """Check if cidr is not too broad and cover public networks."""
    return ipaddress.ip_network(cidr).is_private


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
            cidrs.add(covering_cidr(pod_ips))

    valid_cidrs = filter(is_private_cidr, cidrs)
    return list(valid_cidrs)


def serviceCIDR(runner: Runner):
    """
    Get cluster service IP range.
    """
    serviceCIDR = cluster_serviceCIDR(runner)
    if not serviceCIDR:
        return guess_serviceCIDR(runner)
    return serviceCIDR


def cluster_serviceCIDR(runner: Runner) -> Optional[str]:
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
                    return param.split("=", 1)[1]
            return None
    return None


def guess_serviceCIDR(runner: Runner) -> str:
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
