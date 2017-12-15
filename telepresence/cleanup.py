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
        self.subprocesses = {}  # type: Dict[Popen,Callable]
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
        """Killall all registered subprocesses."""
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
    while True:
        sleep(0.1)
        if main_process.poll() is not None:
            # Shell exited, we're done. Automatic shutdown cleanup will kill
            # subprocesses.
            raise SystemExit(main_process.poll())
        dead_process = processes.any_dead()
        if dead_process:
            # Unfortunatly torsocks doesn't deal well with connections
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
            raise SystemExit(3)
