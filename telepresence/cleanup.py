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

import atexit
import sys
from subprocess import Popen, TimeoutExpired
from time import sleep
from typing import Optional, Callable, Dict

from telepresence.runner import Runner


def kill_process(process: Popen) -> None:
    """Kill a process, make sure it's a dead."""
    if process.poll() is None:
        process.terminate()
    try:
        process.wait(timeout=1)
    except TimeoutExpired:
        process.kill()
        process.wait()


class Subprocesses(object):
    """Shut down subprocesses on exit."""

    def __init__(self):
        Dict  # Avoid Pyflakes F401
        self.subprocesses = {}  # type: Dict[Popen, Callable]
        atexit.register(self.killall)

    def append(self, process: Popen,
               killer: Optional[Callable] = None) -> None:
        """
        Register another subprocess to be shutdown, with optional callable that
        will kill it.
        """
        if killer is None:

            def kill():
                kill_process(process)

            killer = kill
        self.subprocesses[process] = killer

    def killall(self):
        """Kill all registered subprocesses."""
        for killer in self.subprocesses.values():
            killer()

    def any_dead(self):
        """
        Check if any processes are dead.

        If they're all alive, return None.

        If not, kill the remaining ones and return the failed process' poll()
        result.
        """
        for p in self.subprocesses:
            code = p.poll()
            if code is not None:
                self.killall()
                return p


def wait_for_exit(
    runner: Runner, main_process: Popen, processes: Subprocesses
) -> None:
    """Given Popens, wait for one of them to die."""
    runner.write("Everything launched. Waiting to exit...")
    span = runner.span()
    while True:
        sleep(0.1)
        main_code = main_process.poll()
        if main_code is not None:
            # Shell exited, we're done. Automatic shutdown cleanup will kill
            # subprocesses.
            runner.write(
                "Main process ({})\n exited with code {}.".format(
                    main_process.args, main_code
                )
            )
            span.end()
            runner.set_success(True)
            raise SystemExit(main_code)
        dead_process = processes.any_dead()
        if dead_process:
            # Unfortunately torsocks doesn't deal well with connections
            # being lost, so best we can do is shut down.
            runner.write((
                "A subprocess ({}) died with code {}, " +
                "killed all processes...\n"
            ).format(dead_process.args, dead_process.returncode))
            if sys.stdout.isatty:
                print(
                    "Proxy to Kubernetes exited. This is typically due to"
                    " a lost connection.",
                    file=sys.stderr
                )
            span.end()
            raise SystemExit(3)
