"""
Listen on local ports, forward to k8s Service.

For each Service in k8s that has a port, listen on a local port, and forward
connections to that service. This allows `kubectl port-forward` to access all
services, since they will all be accessible from a pod running this proxy.
"""

import os

from twisted.application.service import Application
from twisted.internet import reactor, endpoints
from twisted.protocols.portforward import ProxyFactory

def _get_service_keys(environment):
    # XXX duplicated in local-telepresence
    # XXX also check for TCPness.
    result = [key for key in environment if key.endswith("_SERVICE_HOST")]
    result.sort(key=lambda s: s[:-len("_SERVICE_HOST")])
    return result


def listen():
    for i, key in enumerate(_get_service_keys(os.environ)):
        host = os.environ[key]
        port = int(os.environ[key[:-4] + "PORT"])
        service = endpoints.TCP4ServerEndpoint(reactor, 2000 + i)
        service.listen(ProxyFactory(host, port))
        print("Connecting port {} to {}:{} ({})".format(2000 + i, host, port, key))


print("Listening...")
listen()
application = Application("go")
