# Original version copyright (c) Twisted Matrix Laboratories.
# See LICENSE for details.

"""
Implementation of the SOCKSv5 protocol.

In additional to standard SOCKSv5 this also implements the Tor SOCKS protocol
extension for DNS lookups.
"""

# python imports
import struct
import string
import socket
import time

# twisted imports
from twisted.internet import reactor, protocol, defer
from twisted.python import log

# other imports
from socks5 import GreetingResponse, GreetingRequest, Request, Response
from socks5 import AUTH_TYPE, RESP_STATUS, REQ_COMMAND
from socks5 import Connection
REQ_COMMAND["RESOLVE"] = 0xF0


class SOCKSv5Outgoing(protocol.Protocol):
    def __init__(self, socks):
        self.socks=socks


    def connectionMade(self):
        peer = self.transport.getPeer()
        self.socks.makeReply(90, 0, port=peer.port, ip=peer.host)
        self.socks.otherConn=self


    def connectionLost(self, reason):
        self.socks.transport.loseConnection()


    def dataReceived(self, data):
        self.socks.write(data)


    def write(self,data):
        self.socks.log(self,data)
        self.transport.write(data)



class SOCKSv5Incoming(protocol.Protocol):
    def __init__(self,socks):
        self.socks=socks
        self.socks.otherConn=self


    def connectionLost(self, reason):
        self.socks.transport.loseConnection()


    def dataReceived(self,data):
        self.socks.write(data)


    def write(self, data):
        self.socks.log(self,data)
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
        event = self.statemachine.recv(data)
        if event == "NeedMoreData":
            return
        if event == "GreetingRequest":
            response_event = GreetingResponse(AUTH_TYPE["NO_AUTH"])
            response_data = self.statemachine.send(response_event)
            self.transport.write(response_data)
            return
        if event == "Request":
            if event.someting == REQ_COMMAND["CONNECT"]:
                d = self.connectClass(server, port, SOCKSv5Outgoing, self)
                d.addErrback(lambda result, self = self: self.makeReply(Response())
            elif event.someting == REQ_COMMAND["RESOLVE"]:
                self.makeReply


    def connectionLost(self, reason):
        if self.otherConn:
            self.otherConn.transport.loseConnection()


    def connectClass(self, host, port, klass, *args):
        return protocol.ClientCreator(reactor, klass, *args).connectTCP(host,port)


    def listenClass(self, port, klass, *args):
        serv = reactor.listenTCP(port, klass(*args))
        return defer.succeed(serv.getHost()[1:])


    def makeReply(self, response):
        self.transport.write(self.statemachine.send(response))
        if response.status != RESP_STATUS["SUCCESS"]:
            self.transport.loseConnection()


    def write(self,data):
        self.log(self,data)
        self.transport.write(data)


    def log(self,proto,data):
        if not self.logging: return
        peer = self.transport.getPeer()
        their_peer = self.otherConn.transport.getPeer()
        f=open(self.logging,"a")
        f.write("%s\t%s:%d %s %s:%d\n"%(time.ctime(),
                                        peer.host,peer.port,
                                        ((proto==self and '<') or '>'),
                                        their_peer.host,their_peer.port))
        while data:
            p,data=data[:16],data[16:]
            f.write(string.join(map(lambda x:'%02X'%ord(x),p),' ')+' ')
            f.write((16-len(p))*3*' ')
            for c in p:
                if len(repr(c))>3: f.write('.')
                else: f.write(c)
            f.write('\n')
        f.write('\n')
        f.close()



class SOCKSv5Factory(protocol.Factory):
    """
    A factory for a SOCKSv5 proxy.

    Constructor accepts one argument, a log file name.
    """
    def __init__(self, log):
        self.logging = log


    def buildProtocol(self, addr):
        return SOCKSv5(self.logging, reactor)



class SOCKSv5IncomingFactory(protocol.Factory):
    """
    A utility class for building protocols for incoming connections.
    """
    def __init__(self, socks, ip):
        self.socks = socks
        self.ip = ip


    def buildProtocol(self, addr):
        if addr[0] == self.ip:
            self.ip = ""
            self.socks.makeReply(90, 0)
            return SOCKSv5Incoming(self.socks)
        elif self.ip == "":
            return None
        else:
            self.socks.makeReply(91, 0)
            self.ip = ""
            return None
