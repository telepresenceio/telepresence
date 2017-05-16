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

from twisted.application.service import Application
from twisted.internet import reactor, defer
from twisted.names import client, dns, error, server

import socks


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

    def _got_ip(self, query, ip):
        """
        Generate the response to a query, given an IP.
        """
        name = query.name.name
        answer = dns.RRHeader(name=name, payload=dns.Record_A(address=ip))
        answers = [answer]
        authority = []
        additional = []
        return answers, authority, additional

    def query(self, query, timeout=None):
        if query.type == dns.A:
            print("A query: {}".format(query.name.name))
            d = reactor.resolve(query.name.name)
            d.addCallback(
                lambda ip: self._got_ip(query, ip)
            ).addErrback(
                lambda f: defer.fail(error.DomainError(str(f)))
            )
            return d
        elif query.type == dns.AAAA:
            # Can't do IPv6, just fail fast:
            return defer.fail(error.DomainError())
        else:
            return self.fallback.query(query, timeout=timeout)


def listen():
    reactor.listenTCP(9050, socks.SOCKSv5Factory())
    factory = server.DNSServerFactory(clients=[LocalResolver()])
    protocol = dns.DNSDatagramProtocol(controller=factory)

    reactor.listenUDP(9053, protocol)


print("Listening...")
listen()
application = Application("go")
