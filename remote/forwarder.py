"""
Listen on local ports, forward to k8s Service.

For each Service in k8s that has a port, listen on a local port, and forward
connections to that service. This allows `kubectl port-forward` to access all
services, since they will all be accessible from a pod running this proxy.
"""

from os import environ

from twisted.internet import reactor, endpoints
from twisted.protocols.portforward import ProxyFactory


def listen():
    i = 0
    for key in environ:
        if not key.endswith("_SERVICE_HOST"):
            continue
        # XXX also check for TCPness.
        host = environ[key]
        port = int(environ[key[:-4] + "PORT"])
        endpoints.TCP4ServerEndpoint(reactor, 2000 + i).listen(
            ProxyFactory(host, port))
        i += 1


def main():
    listen()
    reactor.run()


if __name__ == '__main__':
    main()
