# Original version copyright (c) Twisted Matrix Laboratories.
# See LICENSE for details.
"""
Implementation of the SOCKSv5 protocol.

In additional to standard SOCKSv5 this also implements the Tor SOCKS protocol
extension for DNS lookups.

References:

https://www.ietf.org/rfc/rfc1928.txt
https://github.com/dgoulet/torsocks/blob/master/doc/socks/socks-extensions.txt
for RESOLVE extension.
"""

# python imports
import socket

# twisted imports
from twisted.internet import reactor, protocol, defer
from twisted.python import log

# other imports
from socks5 import GreetingResponse, Response
from socks5 import AUTH_TYPE, RESP_STATUS, REQ_COMMAND
from socks5 import Connection
REQ_COMMAND["RESOLVE"] = 0xF0


class SOCKSv5Outgoing(protocol.Protocol):
    def __init__(self, socks):
        self.socks = socks

    def connectionMade(self):
        peer = self.transport.getPeer()
        # XXX This is wrong, should be local side's host and port!
        # https://github.com/mike820324/socks5/issues/16
        self.socks.makeReply(Response(0, 1, peer.host, peer.port))
        self.socks.otherConn = self

    def connectionLost(self, reason):
        self.socks.transport.loseConnection()

    def dataReceived(self, data):
        self.socks.write(data)

    def write(self, data):
        self.transport.write(data)


class SOCKSv5(protocol.Protocol):
    """
    An implementation of the SOCKSv5 protocol.

    @type logging: L{str} or L{None}
    @ivar logging: If not L{None}, the name of the logfile to which connection
        information will be written.

    @type reactor: object providing L{twisted.internet.interfaces.IReactorTCP}
    @ivar reactor: The reactor used to create connections.

    @type buf: L{str}
    @ivar buf: Part of a SOCKSv5 connection request.

    @type otherConn: C{SOCKSv5Incoming}, C{SOCKSv5Outgoing} or L{None}
    @ivar otherConn: Until the connection has been established, C{otherConn} is
        L{None}. After that, it is the proxy-to-destination protocol instance
        along which the client's connection is being forwarded.
    """

    def __init__(self, logging=None, reactor=reactor):
        self.logging = logging
        self.reactor = reactor

    def connectionMade(self):
        self.statemachine = Connection(our_role="server")
        self.statemachine.initiate_connection()
        self.otherConn = None

    def dataReceived(self, data):
        """
        Called whenever data is received.

        @type data: L{bytes}
        @param data: Part or all of a SOCKSv5 packet.
        """
        print("RECEIVED:", repr(data))
        if self.otherConn is not None:
            self.otherConn.write(data)
            return

        event = self.statemachine.recv(data)
        if event == "NeedMoreData":
            return
        if event == "GreetingRequest":
            response_event = GreetingResponse(AUTH_TYPE["NO_AUTH"])
            response_data = self.statemachine.send(response_event)
            self.write(response_data)
            return

        def got_error(e):
            log.err(e)
            self.makeReply(Response(1, event.atyp, event.addr, event.port))

        if event == "Request":
            if event.cmd == REQ_COMMAND["CONNECT"]:
                d = self.connectClass(
                    str(event.addr), event.port, SOCKSv5Outgoing, self
                )
                d.addErrback(got_error)
            elif event.cmd == REQ_COMMAND["RESOLVE"]:

                def write_response(addr):
                    self.write(b"\5\0\0\1" + socket.inet_aton(addr))
                    self.transport.loseConnection()

                self.reactor.resolve(
                    event.addr
                ).addCallback(write_response).addErrback(got_error)

    def connectionLost(self, reason):
        if self.otherConn:
            self.otherConn.transport.loseConnection()

    def connectClass(self, host, port, klass, *args):
        return protocol.ClientCreator(reactor, klass, *args
                                      ).connectTCP(host, port)

    def listenClass(self, port, klass, *args):
        serv = reactor.listenTCP(port, klass(*args))
        return defer.succeed(serv.getHost()[1:])

    def makeReply(self, response):
        self.write(self.statemachine.send(response))
        if response.status != RESP_STATUS["SUCCESS"]:
            self.transport.loseConnection()

    def write(self, data):
        print("SENT:", repr(data))
        self.transport.write(data)


class SOCKSv5Factory(protocol.Factory):
    """
    A factory for a SOCKSv5 proxy.

    Constructor accepts one argument, a log file name.
    """

    def buildProtocol(self, addr):
        return SOCKSv5(reactor)


if __name__ == '__main__':
    from twisted.python.failure import startDebugMode
    startDebugMode()
    reactor.listenTCP(9050, SOCKSv5Factory())
    reactor.run()
