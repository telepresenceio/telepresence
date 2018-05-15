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

from http.server import HTTPServer, BaseHTTPRequestHandler
from threading import Thread

from telepresence.cleanup import BackgroundBase


class DumbHandler(BaseHTTPRequestHandler):
    """
    HTTP handler that returns success for any HEAD request
    """

    tel_output = print

    def do_HEAD(self) -> None:
        "Handle head"
        self.send_response(200)
        self.end_headers()

    def log_message(self, format: str, *args) -> None:
        """
        Make sure log messages go to the right place
        """
        message = format % args
        if message == '"HEAD / HTTP/1.1" 200 -':
            message = "(proxy checking local liveness)"
        self.tel_output(message)


class LocalServer(BackgroundBase):
    """Dumb HTTP server for the proxy pod to poll."""

    def __init__(self, port: int, output) -> None:
        DumbHandler.tel_output = output.write
        self.server = HTTPServer(("127.0.0.1", port), DumbHandler)
        self.thread = Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def poll(self) -> None:
        "Act slighly like an instance of Popen"
        return None

    def kill(self) -> None:
        "Shut down the dumb server"
        self.server.shutdown()  # Block <= 0.5 sec until server is dead
        self.thread.join()  # Should not block
