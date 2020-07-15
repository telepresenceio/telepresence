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
"""
SOCKS proxy + DNS repeater.

The SOCKS proxy implements the tor extensions; it is used by LD_PRELOAD
mechanism (torsocks).

The DNS server handles A records by resolving them the way a DNS client would.
That means e.g. "kubernetes" can eventually be mapped to
"kubernetes.default.svc.cluster.local". This is used by VPN-y mechanisms like
sshuttle in order to make forwarded DNS queries work in way that matches
clients within a k8s pod.
"""

import os
import re
import typing

from twisted.application.service import Application
from twisted.internet import reactor
from twisted.names import dns, server

import periodic
import resolver
import socks

NAMESPACE_PATH = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"


def _get_env_namespace() -> typing.Optional[str]:
    print("Retrieving this pod's namespace from the process environment")
    try:
        return os.environ["TELEPRESENCE_CONTAINER_NAMESPACE"]
    except KeyError:
        print("Failed: TELEPRESENCE_CONTAINER_NAMESPACE not set")
    return None


def _get_sa_namespace() -> typing.Optional[str]:
    print("Reading this pod's namespace from the k8s service account")
    try:
        return open(NAMESPACE_PATH).read()
    except IOError as exc:
        print("Failed to determine namespace from service account:")
        print("  {}".format(exc))
    return None


def _guess_namespace() -> typing.Optional[str]:
    print("Guessing this pod's namespace via /etc/resolv.conf")
    try:
        contents = open("/etc/resolv.conf").read()
    except IOError as exc:
        print("Guess attempt failed:")
        print("  {}".format(exc))
        return None
    for line in contents.splitlines():
        line = line.strip()
        if not line.startswith("search"):
            continue
        match = re.search(r"\s([a-z0-9]+)[.]svc([.]|\s|$)", line)
        if not match:
            continue
        return match.group(1)
    print("Guess attempt failed: No matching search line found.")
    return None


def listen(client):
    reactor.listenTCP(9050, socks.SOCKSv5Factory())
    factory = server.DNSServerFactory(clients=[client])
    protocol = dns.DNSDatagramProtocol(controller=factory)

    reactor.listenUDP(9053, protocol)


def main():
    namespace = _get_env_namespace()
    if namespace is None:
        namespace = _get_sa_namespace()
    if namespace is None:
        namespace = _guess_namespace()
    if namespace is None:
        print("\nERROR: Failed to determine namespace")
        print("Enable serviceaccount access via")
        print("  automountServiceAccountToken: true")
        print("in your Deployment")
        print("or set the TELEPRESENCE_CONTAINER_NAMESPACE env var")
        print("directly or using the Downward API.\n")
        exit("ERROR: Failed to determine namespace")
    print("Pod's namespace is {!r}".format(namespace))
    telepresence_nameserver = os.environ.get("TELEPRESENCE_NAMESERVER")
    telepresence_local_names = os.environ.get("TELEPRESENCE_LOCAL_NAMES")
    reactor.suggestThreadPoolSize(50)
    periodic.setup(reactor)
    print("Listening...")
    listen(resolver.LocalResolver(
        telepresence_nameserver, namespace, telepresence_local_names
    ))


main()
application = Application("go")
