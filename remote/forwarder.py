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

import socket

from twisted.application.service import Application
from twisted.internet import reactor, defer
from twisted.internet.threads import deferToThread
from twisted.names import client, dns, error, server

import socks


def resolve(hostname):
    """Do A record lookup, return list of IPs."""
    return socket.gethostbyname_ex(hostname)[2]


class LocalResolver(object):
    """
    A resolver which uses client-side DNS resolution to resolve A queries.

    This means that queries that wouldn't usually go through DNS will be
    returned. In particular, things like search/ndots in resolv.conf will be
    taken into account when doing the lookup.

    This will run in the pod, and we can send it queries and see what a client
    application running in the pod would get if they ran `gethostbyname()` or
    the like. This is a superset of what a DNS query would return!
    """

    def __init__(self):
        # The default Twisted client.Resolver *almost* does what we want...
        # except it doesn't support ndots! So we manually deal with A records
        # and pass the rest on to client.Resolver.
        self.fallback = client.Resolver(resolv='/etc/resolv.conf')
        # Suffix set by resolv.conf search/domain line, which we remove once we
        # figure out what it is.
        self.suffix = []

    def _got_ips(self, query, ips, record_type):
        """
        Generate the response to a query, given an IP.
        """
        name = query.name.name
        print("Result for {} is {}".format(name, ips))
        answers = [
            dns.RRHeader(name=name, payload=record_type(address=ip))
            for ip in ips
        ]
        authority = []
        additional = []
        return answers, authority, additional

    def _got_error(self, failure):
        print(failure)
        return defer.fail(error.DomainError(str(failure)))

    def query(self, query, timeout=None):
        if query.type == dns.A:
            print("A query: {}".format(query.name.name))

            # We use a special marker hostname, which is always sent by
            # telepresence, to figure out the search suffix set by the client
            # machine's resolv.conf. We then remove it since it masks our
            # ability to add the Kubernetes suffixes. E.g. if DHCP sets 'search
            # wework.com' we want to lookup 'kubernetes' if we get
            # 'kubernetes.wework.com'.
            parts = query.name.name.split(b".")
            if parts[0] == b"hellotelepresence" and not self.suffix:
                self.suffix = parts[1:]
                return self._got_ips(query, [b"127.0.0.1"], dns.Record_A)
            if parts[-len(self.suffix):] == self.suffix:
                parts = parts[:-len(self.suffix)]
                query.name.name = b".".join(parts)
                print("Updated A query: {}".format(query.name.name))

            d = deferToThread(resolve, query.name.name)
            d.addCallback(lambda ips: self._got_ips(query, ips, dns.Record_A)
                          ).addErrback(self._got_error)
            return d
        else:
            print("{} query:".format(query.type, query.name.name))
            return self.fallback.query(query, timeout=timeout)


def listen():
    reactor.listenTCP(9050, socks.SOCKSv5Factory())
    factory = server.DNSServerFactory(clients=[LocalResolver()])
    protocol = dns.DNSDatagramProtocol(controller=factory)

    reactor.listenUDP(9053, protocol)


print("Listening...")
listen()
application = Application("go")
