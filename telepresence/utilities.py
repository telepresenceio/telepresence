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

import os
import shlex
import socket
from subprocess import Popen, TimeoutExpired
from time import time
from typing import Iterable, List


def dumb_print(message: str) -> None:
    """A simplified print that is type-compatible with runner.show"""
    print(message)


def random_name() -> str:
    """Return a random name for a container."""
    return "telepresence-{}-{}".format(time(), os.getpid()).replace(".", "-")


def find_free_port() -> int:
    """
    Find a port that isn't in use.

    XXX race condition-prone.
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        s.bind(("", 0))
        res = s.getsockname()[1]  # type: int
        return res
    finally:
        s.close()


def get_resolv_conf_nameservers() -> List[str]:
    """Return list of nameserver IPs in /etc/resolv.conf."""
    result = []
    with open("/etc/resolv.conf") as f:
        for line in f:
            parts = line.lower().split()
            if len(parts) >= 2 and parts[0] == 'nameserver':
                result.append(parts[1])
    return result


def get_alternate_nameserver() -> str:
    """Get a public nameserver that isn't in /etc/resolv.conf."""
    banned = get_resolv_conf_nameservers()
    # From https://www.lifewire.com/free-and-public-dns-servers-2626062 -
    public = [
        "8.8.8.8", "8.8.4.4", "216.146.35.35", "216.146.36.36", "209.244.0.3",
        "209.244.0.4", "64.6.64.6", "64.6.65.6"
    ]
    for nameserver in public:
        if nameserver not in banned:
            return nameserver
    raise RuntimeError("All known public nameservers are in /etc/resolv.conf.")


def str_command(args: Iterable[str]) -> str:
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


def kill_process(process: "Popen[str]") -> None:
    """Kill a process, make sure it's a dead."""
    if process.poll() is None:
        process.terminate()
    try:
        process.wait(timeout=1)
    except TimeoutExpired:
        process.kill()
        process.wait()
