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
from subprocess import CalledProcessError, TimeoutExpired
from typing import List

from telepresence.connect import SSH
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
        pods = json.loads(
            runner.get_output(
                runner.kubectl(
                    "get", "pods", "--all-namespaces", "-o", "json"
                )
            )
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


def get_sshuttle_command(ssh: SSH, method: str = "auto") -> List[str]:
    return [
        "sshuttle-telepresence",
        "-v",
        "--dns",
        "--method",
        method,
        "-e",
        "ssh {}".format(" ".join(ssh.required_args)),
        "-r",
        "{}:{}".format(ssh.user_at_host, ssh.port),
    ]


def dns_lookup(runner: Runner, name: str, timeout: int) -> bool:
    """
    Performs the requested DNS lookup and returns success or failure
    """
    code = "import socket; socket.gethostbyname(\"{}\")".format(name)
    try:
        # Do the DNS lookup in a subprocess, as this is an easy way to put a
        # timeout on DNS. But discard the output, as it's just noise in the
        # logs anyhow. Use get_output to avoid logging.
        runner.get_output(
            ["python3", "-c", code],
            stderr_to_stdout=True,
            timeout=timeout,
        )
        return True
    except (CalledProcessError, TimeoutExpired):
        pass
    return False


def connect_sshuttle(
    runner: Runner, remote_info: RemoteInfo, hosts_or_ips: List[str], ssh: SSH
) -> None:
    """Connect to Kubernetes using sshuttle."""
    span = runner.span()
    sshuttle_method = "auto"
    if runner.platform == "linux":
        # sshuttle tproxy mode seems to have issues:
        sshuttle_method = "nat"
    runner.launch(
        "sshuttle",
        get_sshuttle_command(ssh, sshuttle_method) + [
            # DNS proxy running on remote pod:
            "--to-ns",
            "127.0.0.1:9053",
        ] + get_proxy_cidrs(runner, remote_info, hosts_or_ips),
        keep_session=True,  # Avoid trouble with interactive sudo
    )

    # sshuttle will take a while to startup. We can detect it being up when
    # DNS resolution of services starts working. We use a specific single
    # segment so any search/domain statements in resolv.conf are applied,
    # which then allows the DNS proxy to detect the suffix domain and
    # filter it out.
    # On Macs, and perhaps elsewhere, there is OS-level caching of
    # NXDOMAIN, so bypass caching by sending new domain each time. Another,
    # less robust alternative, is to `killall -HUP mDNSResponder`.
    subspan = runner.span("sshuttle-wait")
    countdown = 3
    for idx in runner.loop_until(35, 0.1):
        # Construct a different name each time to avoid NXDOMAIN caching.
        name = "hellotelepresence-{}".format(idx)
        runner.write("Wait for vpn-tcp connection: {}".format(name))
        if dns_lookup(runner, name, 5):
            countdown -= 1
            runner.write("Resolved {}. {} more...".format(name, countdown))
            if countdown == 0:
                break
        # The loop uses a single segment to try to capture suffix or search
        # path in the proxy. However, in some network setups, single-segment
        # names don't get resolved the normal way. To see whether we're running
        # into this, also try to resolve a name with many dots. This won't
        # resolve successfully but will show up in the logs. See also:
        # https://github.com/telepresenceio/telepresence/issues/242. We use a
        # short timeout here because (1) this takes a long time for some users
        # and (2) we're only looking for a log entry; we don't expect this to
        # succeed and don't benefit from waiting for the NXDOMAIN.
        many_dotted_name = "{}.a.sanity.check.telepresence.io".format(name)
        dns_lookup(runner, many_dotted_name, 1)

    if countdown != 0:
        runner.add_cleanup("Diagnose vpn-tcp", log_info_vpn_crash, runner)
        raise RuntimeError("vpn-tcp tunnel did not connect")

    subspan.end()
    span.end()


def log_info_vpn_crash(runner: Runner) -> None:
    """
    Log some stuff that may help diagnose vpn-tcp method failures.
    """
    commands = [
        "ls -l /etc/resolv.conf",
        "grep -v ^# /etc/resolv.conf",
        "ls -l /etc/resolvconf",
        "cat /etc/nsswitch.conf",
        "ls -l /etc/resolver",
    ]
    for command in commands:
        try:
            runner.check_call(command.split(), timeout=1)
        except (CalledProcessError, TimeoutExpired):
            pass
