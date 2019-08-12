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

from subprocess import CalledProcessError
from typing import List

from telepresence.runner import Runner


class SSH(object):
    """Run ssh to k8s-proxy with appropriate arguments."""
    def __init__(
        self,
        runner: Runner,
        port: int,
        user_at_host: str = "telepresence@127.0.0.1",
    ) -> None:
        self.runner = runner
        self.port = port
        self.user_at_host = user_at_host
        self.required_args = [
            # Ignore local configuration (~/.ssh/config)
            "-F",
            "/dev/null",
            # Don't validate host key:
            "-oStrictHostKeyChecking=no",
            # Don't store host key:
            "-oUserKnownHostsFile=/dev/null",
        ]

    def command(
        self, additional_args: List[str], prepend_arguments: List[str] = []
    ) -> List[str]:
        """
        Return command line argument list for running ssh.

        Takes command line arguments to run on remote machine, and optional
        arguments to ssh itself.
        """
        return ["ssh"] + prepend_arguments + self.required_args + [
            # SSH with no warnings:
            "-vv" if self.runner.verbose else "-q",
            "-p",
            str(self.port),
            self.user_at_host,
        ] + additional_args

    def bg_command(self, additional_args: List[str]) -> List[str]:
        """
        Return command line argument list for running ssh for port forwards.
        """
        return self.command(
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

    def wait(self) -> bool:
        """Return whether SSH server can be reached within 30 seconds."""
        for _ in self.runner.loop_until(30, 0.25):
            try:
                self.runner.check_call(self.command(["/bin/true"]), timeout=5)
                return True
            except CalledProcessError:
                pass
        return False
