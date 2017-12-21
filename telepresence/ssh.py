from subprocess import Popen, CalledProcessError
from time import time, sleep
from typing import List

from telepresence.runner import Runner


class SSH(object):
    """Run ssh to k8s-proxy with appropriate arguments."""

    def __init__(
        self, runner: Runner, port: int, host: str = "localhost"
    ) -> None:
        self.runner = runner
        self.port = port
        self.host = host

    def command(
        self, additional_args: List[str], prepend_arguments: List[str] = []
    ) -> List[str]:
        """
        Return command line argument list for running ssh.

        Takes command line arguments to run on remote machine, and optional
        arguments to ssh itself.
        """
        return ["ssh"] + prepend_arguments + [
            # Ignore local configuration (~/.ssh/config)
            "-F",
            "/dev/null",
            # SSH with no warnings:
            "-vv" if self.runner.verbose else "-q",
            # Don't validate host key:
            "-oStrictHostKeyChecking=no",
            # Don't store host key:
            "-oUserKnownHostsFile=/dev/null",
            "-p",
            str(self.port),
            "telepresence@" + self.host,
        ] + additional_args

    def popen(self, additional_args: List[str]) -> Popen:
        """Connect to remote pod via SSH.

        Returns Popen object.
        """
        return self.runner.popen(
            self.command(
                additional_args,
                [
                    # No remote command, since this intended for things like -L
                    # or -R where we don't want to run a remote command.
                    "-N",
                    # Ping once a second; after ten retries will disconnect:
                    "-oServerAliveInterval=1",
                    "-oServerAliveCountMax=10",
                ]
            )
        )

    def wait(self) -> None:
        """Return when SSH server can be reached."""
        start = time()
        while time() - start < 30:
            try:
                self.runner.check_call(self.command(["/bin/true"]))
            except CalledProcessError:
                sleep(0.25)
            else:
                return
        raise RuntimeError("SSH isn't starting.")
