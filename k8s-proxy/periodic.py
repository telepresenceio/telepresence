# Copyright 2018 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""
Perform periodic operations
"""

from twisted.internet.task import LoopingCall
from twisted.logger import Logger
from twisted.web.client import Agent, HTTPConnectionPool, _HTTP11ClientFactory


class QuietHTTP11ClientFactory(_HTTP11ClientFactory):
    """
    https://stackoverflow.com/questions/18670252/how-to-suppress-noisy-factory-started-stopped-log-messages-from-twisted
    """
    noisy = False


class Poll(object):
    """
    Poll the Telepresence client
    - Log periodically so `kubectl logs` doesn't go idle
    - Generate traffic so `kubectl port-forward` doesn't go idle
    """

    def __init__(self, reactor):
        self.reactor = reactor
        self.log = Logger("Poll")
        pool = HTTPConnectionPool(reactor)
        pool._factory = QuietHTTP11ClientFactory
        self.agent = Agent(reactor, connectTimeout=10.0, pool=pool)

    def periodic(self):
        """Periodically query the client"""
        deferred = self.agent.request(b"HEAD", b"http://localhost:9055/")
        deferred.addCallback(self.success)
        deferred.addErrback(self.failure)

    def success(self, response):
        """Client is still there"""
        if response.code == 200:
            self.log.info("Checkpoint")
        else:
            self.log.warn("Client returned code {}".format(response.code))

    def failure(self, failure):
        """Client is not there"""
        self.log.error("Failed to contact Telepresence client:")
        self.log.error(failure.getErrorMessage())
        self.log.warn("Perhaps it's time to exit?")


def setup(reactor):
    """
    Set up periodict tasks
    """
    poller = Poll(reactor)
    periodic_task = LoopingCall(poller.periodic)
    periodic_task.start(3, True)
