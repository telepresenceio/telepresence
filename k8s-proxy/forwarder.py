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
import socket
from copy import deepcopy
from typing import Callable, List, Tuple, Optional, Union

from twisted.application.service import Application
from twisted.internet import reactor, defer
from twisted.internet.threads import deferToThread
from twisted.names import client, dns, error, server

import socks

DNSQueryResult = Union[defer.Deferred, Tuple[List[dns.RRHeader], List, List]]


def resolve(hostname: str) -> List[str]:
    """Do A record lookup, return list of IPs."""
    return socket.gethostbyname_ex(hostname)[2]


# XXX duplicated from telepresence
def get_resolv_conf_namservers() -> List[str]:
    """Return list of namserver IPs in /etc/resolv.conf."""
    result = []
    with open("/etc/resolv.conf") as f:
        for line in f:
            parts = line.lower().split()
            if len(parts) >= 2 and parts[0] == 'nameserver':
                result.append(parts[1])
    return result


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
        if NOLOOP:
            self.kubedns = get_resolv_conf_namservers()[0]
            # We want nameserver that the host machine *doesn't* use so
            # sshuttle doesn't capture packets and cause an infinite query
            # loop:
            self.fallback = client.Resolver(
                servers=[(os.environ["TELEPRESENCE_NAMESERVER"], 53)]
            )
        else:
            self.fallback = client.Resolver(resolv='/etc/resolv.conf')
        # Suffix set by resolv.conf search/domain line, which we remove once we
        # figure out what it is.
        self.suffix = []  # type: List[bytes]

    def _got_ips(self, name: bytes, ips: List[str],
                 record_type: Callable) -> DNSQueryResult:
        """
        Generate the response to a query, given an IP.
        """
        print("Result for {} is {}".format(name, ips))
        answers = [
            dns.RRHeader(name=name, payload=record_type(address=ip))
            for ip in ips
        ]
        authority = []  # type: List
        additional = []  # type: List
        return answers, authority, additional

    def _got_error(self, failure) -> defer.Deferred:
        print(failure)
        return defer.fail(error.DomainError(str(failure)))

    def _no_loop_kube_query(
        self, query: dns.Query, timeout: float, real_name: bytes
    ) -> DNSQueryResult:
        """
        Do a query to Kube DNS for Kubernetes records only, fall back to
        random DNS server if that fails.
        """
        new_query = deepcopy(query)
        if not query.name.name.endswith(b".local"):
            parts = query.name.name.split(b".")
            if len(parts) == 1:
                parts.append(NAMESPACE.encode("ascii"))
            assert len(parts) == 2
            new_query.name.name = b".".join(parts) + b".svc.cluster.local"

        def fallback(err):
            print(
                "FAILED to lookup {} ({}), trying {}".
                format(new_query.name.name, err, query.name.name)
            )
            return self.fallback.query(query, timeout=timeout)

        def fix_names(result):
            # Make sure names in response match what the client asked format
            for answer in result[0]:
                answer.name = dns.Name(real_name)
            print("RESULT: {}".format(result))
            return result

        print("RESOLVING {}".format(new_query.name.name))
        # We expect Kube DNS to be fast, so have short timeout in case we need
        # to fallback:
        d = client.Resolver(servers=[(self.kubedns, 53)]).query(
            new_query, timeout=[0.1]
        )
        d.addCallback(fix_names)
        d.addErrback(fallback)
        return d

    def query(
        self,
        query: dns.Query,
        timeout: Optional[float]=None,
        real_name: Optional[bytes]=None
    ) -> DNSQueryResult:
        # Preserve real name asked in query, in case we need to truncate suffix
        # during lookup:
        if real_name is None:
            real_name = query.name.name
        # We use a special marker hostname, which is always sent by
        # telepresence, to figure out the search suffix set by the client
        # machine's resolv.conf. We then remove it since it masks our ability
        # to add the Kubernetes suffixes. E.g. if DHCP sets 'search wework.com'
        # on the client machine we will want to lookup 'kubernetes' if we get
        # 'kubernetes.wework.com'.
        parts = query.name.name.split(b".")
        if parts[0].startswith(b"hellotelepresence") and not self.suffix:
            self.suffix = parts[1:]
            print("Set DNS suffix we filter out to: {}".format(self.suffix))
        if parts[0].startswith(b"hellotelepresence"
                               ) and parts[1:] == self.suffix:
            return self._got_ips(real_name, ["127.0.0.1"], dns.Record_A)
        if parts[-len(self.suffix):] == self.suffix:
            new_query = deepcopy(query)
            new_query.name.name = b".".join(parts[:-len(self.suffix)])
            print(
                "Updated query of type {} from {} to {}".
                format(query.type, query.name.name, new_query.name.name)
            )

            def failed(f):
                print(
                    "Failed to lookup {} due to {}, falling back to {}".
                    format(new_query.name.name, f, query.name.name)
                )
                return self.fallback.query(query, timeout=timeout)

            return defer.maybeDeferred(
                self.query,
                new_query,
                timeout=(1, 1),
                real_name=query.name.name,
            ).addErrback(failed)

        # No special suffix:
        if query.type == dns.A:
            print("A query: {}".format(query.name.name))
            # sshuttle, which is running on client side, works by capturing DNS
            # packets to name servers. If we're on a VM, non-Kubernetes domains
            # like google.com won't be handled by Kube DNS and so will be
            # forwarded to name servers that host defined... and then they will
            # be recaptured by sshuttle (depending on how VM networkng is
            # setup) which will send them back here and result in infinite loop
            # of DNS queries. So we check Kube DNS in way that won't trigger
            # that, and if that doesn't work query a name server that sshuttle
            # doesn't know about.
            if NOLOOP:
                # maybe be servicename, service.namespace, or something.local
                # (.local is used for both services and pods):
                if query.name.name.count(b".") in (
                    0, 1
                ) or query.name.name.endswith(b".local"):
                    return self._no_loop_kube_query(
                        query, timeout=timeout, real_name=real_name
                    )
                else:
                    return self.fallback.query(query, timeout=timeout)

            d = deferToThread(resolve, query.name.name)
            d.addCallback(
                lambda ips: self._got_ips(real_name, ips, dns.Record_A)
            ).addErrback(self._got_error)
            return d
        elif query.type == dns.AAAA:
            # Kubernetes can't do IPv6, and if we return empty result OS X
            # gives up (Happy Eyeballs algorithm, maybe?), so never return
            # anything IPv6y. Instead return A records to pacify OS X.
            print(
                "AAAA query, sending back A instead: {}".
                format(query.name.name)
            )
            query.type = dns.A  # type: ignore
            return self.query(query, timeout=timeout, real_name=real_name)
        else:
            print("{} query: {}".format(query.type, query.name.name))
            return self.fallback.query(query, timeout=timeout)


def listen():
    reactor.listenTCP(9050, socks.SOCKSv5Factory())
    factory = server.DNSServerFactory(clients=[LocalResolver()])
    protocol = dns.DNSDatagramProtocol(controller=factory)

    reactor.listenUDP(9053, protocol)


predefined_namespace = os.getenv('TELEPRESENCE_CONTAINER_NAMESPACE', None)
if predefined_namespace:
    NAMESPACE = predefined_namespace
else:
    with open("/var/run/secrets/kubernetes.io/serviceaccount/namespace") as f:
        NAMESPACE = f.read()
NOLOOP = os.environ.get("TELEPRESENCE_NAMESERVER") is not None
reactor.suggestThreadPoolSize(50)
print("Listening...")
listen()
application = Application("go")
