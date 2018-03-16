# Original version copyright (c) Twisted Matrix Laboratories.
# See LICENSE for details.
"""
Tests for L{socks}, an implementation of the SOCKSv5 protocol with Tor
extension.
"""

import struct
import socket

from twisted.internet import defer
from twisted.internet.address import (
    IPv4Address,
)
from twisted.internet.error import DNSLookupError
from twisted.python.compat import iterbytes
from twisted.test import proto_helpers
from twisted.trial import unittest

import socks


class StringTCPTransport(proto_helpers.StringTransport):
    disconnecting = False
    stringTCPTransport_closing = False
    peer = None

    def getPeer(self):
        return self.peer

    def getHost(self):
        return IPv4Address('TCP', '2.3.4.5', 42)

    def loseConnection(self):
        self.stringTCPTransport_closing = True
        self.disconnecting = True


class FakeResolverReactor:
    """
    Bare-bones reactor with deterministic behavior for the resolve method.
    """

    def __init__(self, names):
        """
        @type names: L{dict} containing L{str} keys and L{str} values.
        @param names: A hostname to IP address mapping. The IP addresses are
            stringified dotted quads.
        """
        self.names = names

    def resolve(self, hostname):
        """
        Resolve a hostname by looking it up in the C{names} dictionary.
        """
        try:
            return defer.succeed(self.names[hostname])
        except KeyError:
            return defer.fail(
                DNSLookupError(
                    "FakeResolverReactor couldn't find {}".format(hostname)
                )
            )


class SOCKSv5Driver(socks.SOCKSv5):
    # last SOCKSv5Outgoing instantiated
    driver_outgoing = None

    # last SOCKSv5IncomingFactory instantiated
    driver_listen = None

    def connectClass(self, host, port, klass, *args):
        # fake it
        def got_ip(ip):
            proto = klass(*args)
            transport = StringTCPTransport()
            transport.peer = IPv4Address('TCP', ip, port)
            proto.makeConnection(transport)
            self.driver_outgoing = proto
            return proto

        d = self.reactor.resolve(host)
        d.addCallback(got_ip)
        return d

    def listenClass(self, port, klass, *args):
        # fake it
        factory = klass(*args)
        self.driver_listen = factory
        if port == 0:
            port = 1234
        return defer.succeed(('6.7.8.9', port))


class ConnectTests(unittest.TestCase):
    """
    Tests for SOCKSv5 connect requests using the L{SOCKSv5} protocol.
    """

    def setUp(self):
        self.sock = SOCKSv5Driver()
        transport = StringTCPTransport()
        self.sock.makeConnection(transport)
        self.sock.reactor = FakeResolverReactor({
            "example.com": "5.6.7.8",
            "1.2.3.4": "1.2.3.4"
        })

    def deliver_data(self, protocol, data):
        """
        Deliver bytes one by one, to ensure parser can deal with unchunked
        data.
        """
        for byte in iterbytes(data):
            protocol.dataReceived(byte)

    def assert_handshake(self):
        """The server responds with NO_AUTH to the initial SOCKS5 handshake."""
        self.deliver_data(self.sock, struct.pack("!BBB", 5, 1, 0))
        reply = self.sock.transport.value()
        self.sock.transport.clear()
        self.assertEqual(reply, struct.pack("!BB", 5, 0))

    def assert_connect(
            self,
            connect_address,
            connect_port,
            bound_address,
            bound_port,
    ):
        """The server responds to CONNECT with successful result."""
        # The CONNECT command to an IPv4 address
        self.deliver_data(
            self.sock,
            struct.pack(
                '!BBBB',
                # VER = 5
                5,
                # CMD = 1 (CONNECT)
                1,
                # RSV (Reserved)
                0,
                # ATYP = 1 (IPv4)
                1,
            ) +
            # IP address to connect to
            socket.inet_aton(connect_address) +
            # Port number to connect to
            struct.pack("!H", connect_port)
        )
        reply = self.sock.transport.value()
        self.sock.transport.clear()
        self.assertEqual(
            reply,
            struct.pack(
                '!BBBB',
                # VER (Version)
                5,
                # REP (Reply); 0 = Succeeded.
                0,
                # RSV (Reserved)
                0,
                # ATYP (Address type); 1 = (IPv4)
                1,
            ) +
            # The server-bound address
            socket.inet_aton(bound_address) +
            # The server-bound port number
            struct.pack("!H", bound_port)
        )
        self.assertFalse(self.sock.transport.stringTCPTransport_closing)
        self.assertIsNotNone(self.sock.driver_outgoing)
        self.assertEqual(
            self.sock.driver_outgoing.transport.getPeer(),
            IPv4Address('TCP', connect_address, connect_port)
        )

    def assert_dataflow(self):
        """
        Data flows between client connection and proxied outgoing connection.
        """
        # pass some data through
        self.deliver_data(self.sock, b'hello, world')
        self.assertEqual(
            self.sock.driver_outgoing.transport.value(), b'hello, world'
        )

        # the other way around
        self.sock.driver_outgoing.dataReceived(b'hi there')
        self.assertEqual(self.sock.transport.value(), b'hi there')

    def assert_resolve(self, domainname, address):
        self.deliver_data(
            self.sock,
            struct.pack(
                '!BBBB',
                # VER (Version)
                5,
                # RESOLVE
                0xf0,
                # RSV (Reserved)
                0,
                # ATYP (Address type); 3 = Domain name
                3,
            ) +
            # Length-prefixed domain to resolve.
            struct.pack("!B", len(domainname)) +
            domainname +
            # Arbitrary port required by the protocol but not used for
            # anything.
            struct.pack("!H", 3401)
        )
        reply = self.sock.transport.value()
        self.sock.transport.clear()
        self.assertEqual(
            reply,
            struct.pack('!BBBB', 5, 0, 0, 1) + socket.inet_aton(address)
        )
        self.assertTrue(self.sock.transport.stringTCPTransport_closing)

    def test_simple(self):
        """The server proxies an outgoing connection to an IPv4 address."""
        self.assert_handshake()
        self.assert_connect('1.2.3.4', 34)
        self.assert_dataflow()

        self.sock.connectionLost('fake reason')
        self.assertTrue(
            self.sock.driver_outgoing.transport.stringTCPTransport_closing
        )

    def test_socks5SuccessfulResolution(self):
        """
        Socks5 also supports hostname-based connections.

        @see: U{http://en.wikipedia.org/wiki/SOCKS#SOCKS5}
        """
        self.assert_handshake()
        self.assert_resolve("example.com", "5.6.7.8")

    def test_socks5TorStyleFailedResolution(self):
        """
        A Tor-style name resolution when resolution fails.
        """
        self.assert_handshake()
        self.deliver_data(
            self.sock,
            struct.pack('!BBBB', 5, 0xf0, 0, 3) + struct.pack(
                "!B", len(b"unknown")
            ) + b"unknown" + struct.pack("!H", 3401)
        )
        reply = self.sock.transport.value()
        self.sock.transport.clear()
        self.assertEqual(reply, struct.pack('!BBBB', 5, 4, 0, 0))
        self.assertTrue(self.sock.transport.stringTCPTransport_closing)
        self.assertEqual(len(self.flushLoggedErrors(DNSLookupError)), 1)

    def test_eofRemote(self):
        """If the outgoing connection closes the client connection closes."""
        self.assert_handshake()
        self.assert_connect('1.2.3.4', 34)

        # now close it from the server side
        self.sock.driver_outgoing.connectionLost('fake reason')
        self.assertTrue(self.sock.transport.stringTCPTransport_closing)

    def test_eofLocal(self):
        """If the client connection closes the outgoing connection closes."""
        self.assert_handshake()
        self.assert_connect('1.2.3.4', 34)

        self.sock.connectionLost('fake reason')
        self.assertTrue(
            self.sock.driver_outgoing.transport.stringTCPTransport_closing
        )
