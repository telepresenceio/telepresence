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
    _pattern = 'workstation'
    _network = '172.0.2'

    def _dynamicResponseRequired(self, query):
        """
        Check the query to determine if a dynamic response is required.
        """
        if query.type == dns.A:
            labels = query.name.name.split('.')
            if labels[0].startswith(self._pattern):
                return True

        return False

    def _got_ip(self, ip, query):
        """
        Generate the response to a query, given an IP.
        """
        name = query.name.name
        answer = dns.RRHeader(
            name=name,
            payload=dns.Record_A(address=ip))
        answers = [answer]
        authority = []
        additional = []
        return answers, authority, additional

    def query(self, query, timeout=None):
        if query.type == dns.A:
            d = reactor.resolve(query.name.name)
            d.addCallback(self._got_ip, query)
            return d
        else:
            # this will cause fallback to the next Resolver in the chain:
            return defer.fail(error.DomainError())


def listen():
    reactor.listenTCP(9050, socks.SOCKSv5Factory())
    # The default Twisted client.Resolver *almost* does what we want... except
    # it doesn't support ndots! So we manually deal with A records and pass the
    # rest on to client.Resolver.
    factory = server.DNSServerFactory(
        clients=[LocalResolver(), client.Resolver(resolv='/etc/resolv.conf')]
    )
    protocol = dns.DNSDatagramProtocol(controller=factory)

    reactor.listenUDP(9053, protocol)


print("Listening...")
listen()
application = Application("go")
