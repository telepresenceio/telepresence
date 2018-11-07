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
from abc import ABC, abstractmethod
from http.server import HTTPServer, BaseHTTPRequestHandler
from subprocess import Popen, TimeoutExpired
from threading import Thread
from typing import Optional, Callable, List

from telepresence.utilities import kill_process


class Background(ABC):
    """
    A process or thread running separately from the main thread
    """

    def __init__(self, name: str, killer: Optional[Callable],
                 critical: bool) -> None:
        """
        :param name: Always useful for identification in messages
        :param killer: Optional callable to kill this background thing
        :param critical: Is the death of this item fatal to the session?
        """
        self.name = name
        self.killer = killer
        self.critical = critical

    @property
    @abstractmethod
    def alive(self) -> bool:
        pass

    @abstractmethod
    def join(self, timeout: Optional[float]) -> None:
        pass

    @abstractmethod
    def kill(self) -> None:
        pass

    def __str__(self) -> str:
        return "{} {}".format(self.__class__.__name__, self.name)


class BackgroundThread(Background):
    def __init__(
        self, name, thread: Thread, killer=None, critical=True
    ) -> None:
        super().__init__(name, killer, critical)
        self.thread = thread

    @property
    def alive(self) -> bool:
        return self.thread.is_alive()

    def join(self, timeout: Optional[float] = None) -> None:
        self.thread.join(timeout)
        if self.thread.is_alive():
            assert timeout is not None
            raise TimeoutExpired(["Thread", self.name], timeout)

    def kill(self) -> None:
        assert self.killer is not None
        if self.thread.is_alive():
            self.killer()
        self.thread.join()


class BackgroundProcess(Background):
    def __init__(
        self, name: str, process: Popen, killer=None, critical=True
    ) -> None:
        super().__init__(name, killer, critical)
        self.process = process

    @property
    def alive(self) -> bool:
        return self.process.poll() is None

    def join(self, timeout: Optional[float] = None) -> None:
        self.process.wait(timeout)

    def kill(self) -> None:
        if self.killer is None:
            self.killer = lambda: kill_process(self.process)
        self.killer()
        self.process.wait()


class TrackedBG(object):
    """
    Tracked background processes, threads, etc.
    """

    def __init__(self, runner):
        List  # Avoid Pyflakes F401
        self.runner = runner
        self.subprocesses = []  # type: List[Background]
        runner.add_cleanup("Kill background items", self.killall)

    def append(self, bg: Background) -> None:
        """
        Register a background item to be tracked and eventually shut down
        """
        self.subprocesses.append(bg)
        # Grep-able log: self.runner.write("Tracking {}".format(bg))

    def killall(self):
        """
        Kill all tracked items
        """
        for bg in reversed(self.subprocesses):
            self.runner.write("Killing {}".format(bg))
            bg.kill()

    def which_dead(self) -> List[Background]:
        """
        Return which (if any) background items are dead.
        FIXME: Does not consider critical flag.
        """
        dead_processes = []  # type: List[Background]
        dead_others = []
        for bg in self.subprocesses:
            if not bg.alive:
                if isinstance(bg, BackgroundProcess):
                    dead_processes.append(bg)
                    exit_info = " (exit code {})".format(bg.process.poll())
                else:
                    dead_others.append(bg)
                    exit_info = " (why?)"
                self.runner.write("{} is dead{}".format(bg, exit_info))

        assert not dead_others, dead_others
        return dead_processes + dead_others


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


def launch_local_server(port: int, output) -> Background:
    """
    Make a dumb web server for the proxy pod to poll.
    """
    DumbHandler.tel_output = output.write
    server = HTTPServer(("127.0.0.1", port), DumbHandler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    name = "Web server for proxy poll"
    output.write("Launching " + name)
    return BackgroundThread(name, thread, killer=server.shutdown)
