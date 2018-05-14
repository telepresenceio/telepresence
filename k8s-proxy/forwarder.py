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

from twisted.application.service import Application
from twisted.internet import reactor
from twisted.internet.task import LoopingCall
from twisted.names import dns, server
from twisted.web.client import Agent

import socks
import resolver

NAMESPACE_PATH = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"


def poll_success():
    "Client is still there"
    print("Checkpoint")


def poll_failure():
    "Client is not there"
    print("Failed to contact Telepresence client!")
    print("Perhaps it's time to exit?")


def periodic():
    "Periodically query the client"
    agent = Agent(reactor, connectTimeout=10.0)
    print("Making a request")
    deferred = agent.request("HEAD", b"http://localhost:9055/")
    deferred.addCallback(poll_success)
    deferred.addErrback(poll_failure)
    print("Request made")


def listen(client):
    reactor.listenTCP(9050, socks.SOCKSv5Factory())
    factory = server.DNSServerFactory(clients=[client])
    protocol = dns.DNSDatagramProtocol(controller=factory)

    reactor.listenUDP(9053, protocol)


def main():
    predefined_namespace = os.getenv('TELEPRESENCE_CONTAINER_NAMESPACE', None)
    if predefined_namespace:
        NAMESPACE = predefined_namespace
    else:
        with open(NAMESPACE_PATH) as f:
            NAMESPACE = f.read()
    telepresence_nameserver = os.environ.get("TELEPRESENCE_NAMESERVER")
    reactor.suggestThreadPoolSize(50)

    periodic_task = LoopingCall(periodic)
    periodic_task.start(30, False)

    print("Listening...")
    listen(resolver.LocalResolver(telepresence_nameserver, NAMESPACE))


main()
application = Application("go")
