"""
SOCKS proxy.
"""

from twisted.application.service import Application
from twisted.internet import reactor

import socks


def listen():
    reactor.listenTCP(9050, socks.SOCKSv5Factory())


print("Listening...")
listen()
application = Application("go")
