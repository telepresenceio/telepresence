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

import typing
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread

from .runner import Runner


def dumb_print(message: str) -> None:
    print(message)


class DumbHandler(BaseHTTPRequestHandler):
    """
    HTTP handler that returns success for any HEAD request
    """

    tel_output = dumb_print

    def do_HEAD(self) -> None:
        "Handle head"
        self.send_response(200)
        self.end_headers()

    def log_message(self, format: str, *args: typing.Any) -> None:
        """
        Make sure log messages go to the right place
        """
        message = format % args
        if message == '"HEAD / HTTP/1.1" 200 -':
            message = "(proxy checking local liveness)"
        DumbHandler.tel_output(message)


def launch_local_server(runner: Runner, port: int) -> None:
    """
    Make a dumb web server for the proxy pod to poll.
    """
    DumbHandler.tel_output = runner.write
    server = HTTPServer(("127.0.0.1", port), DumbHandler)
    Thread(target=server.serve_forever, daemon=True).start()
    name = "Web server for proxy poll"
    runner.write("Launching " + name)
    runner.add_cleanup("Kill " + name, server.shutdown)
