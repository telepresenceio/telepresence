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

import shlex
from subprocess import check_output
from typing import List


# Copy-pasta from telepresence.utilities
def str_command(args: List[str]) -> str:
    """
    Return a string representing the shell command and its arguments.

    :param args: Shell command and its arguments
    :return: String representation thereof
    """
    res = []
    for arg in args:
        if "\n" in arg:
            res.append(repr(arg))
        else:
            res.append(shlex.quote(arg))
    return " ".join(res)


class Container(object):
    """
    Run commands in a container
    FIXME: This should be a context manager
    """

    def __init__(self, image: str, verbose=True) -> None:
        self.image = image
        self.verbose = verbose
        self.container = "CNTNR"
        docker = "docker run --rm -d".split()
        infinite = "tail -f /dev/null".split()
        res = self._run(docker + [self.image] + infinite)
        self.container = res.strip()

    def __del__(self):
        self._run(["docker", "kill", self.container])

    def _run(self, *args, **kwargs) -> str:
        "Run a command"
        if self.verbose:
            cmd = str_command(args[0])
            if self.container:
                cmd = cmd.replace(self.container, "CNTNR")
            print("+ {}".format(cmd))
        res_bytes = check_output(*args, **kwargs)
        res = str(res_bytes, "utf-8")
        if self.verbose and res.rstrip():
            print(res.rstrip())
        return res

    def execute(self, args: List[str], cwd="/") -> str:
        "Run a command in the container"
        cmd = ["docker", "exec", "-w", cwd, self.container] + args
        return self._run(cmd)

    def execute_sh(self, command: str, **kwargs) -> str:
        "Run a command passed as a string"
        return self.execute(shlex.split(command), **kwargs)

    def copy_from(self, source: str, target: str):
        "Copy files from the container"
        args = ["docker", "cp", "{}:{}".format(self.container, source), target]
        self._run(args)

    def copy_to(self, source: str, target: str):
        "Copy files to the container"
        args = ["docker", "cp", source, "{}:{}".format(self.container, target)]
        self._run(args)
