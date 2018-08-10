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
from pathlib import Path
from typing import List

from telepresence.runner import Runner

NICE_FAILURE = """\
#!/bin/sh
echo {} is not supported under Telepresence.
echo See \
https://telepresence.io/reference/limitations.html \
for details.
exit 55
"""


def make_unsupported_tool(commands: List[str], destination: Path) -> None:
    """
    Create replacement command-line tools that just error out, in a nice way.
    """
    for command in commands:
        path = destination / command
        path.write_text(NICE_FAILURE.format(command))
        path.chmod(0o755)


def make_sip_workaround_copy(protected: List[Path], destination: Path) -> None:
    """
    Work around System Integrity Protection.

    Newer OS X don't allow injecting libraries into binaries in /bin, /sbin and
    /usr. We therefore make a copy of them and modify $PATH to point at their
    new location. It's only ~100MB so this should be pretty fast!

    Copy binaries from protected paths to an unprotected path
    """
    for directory in protected:
        for command in directory.iterdir():
            target = destination / command.name
            try:
                data = command.read_bytes()
                target.write_bytes(data)
                target.chmod(0o775)
            except IOError:
                continue


def apply_workarounds(
    runner: Runner, original_path: str, replace_dns_tools: bool
) -> str:
    """
    Apply workarounds by creating required executables and returning an updated
    PATH variable for the user process.

    :param runner: Runner
    :param original_path: Current $PATH
    :param replace_dns_tools: True for inject-tcp, where DNS is not proxied
    :param work_around_sip: True for inject-tcp on the Mac
    :return: Updated $PATH
    """
    paths = original_path.split(os.pathsep)

    if runner.platform == "darwin":
        # Capture protected $PATH entries in order
        protected_set = {"/bin", "/sbin", "/usr/sbin", "/usr/bin"}
        protected = [Path(path) for path in paths if path in protected_set]

        # Make copies in an unprotected location
        sip_bin = runner.make_temp("sip_bin")
        make_sip_workaround_copy(protected, sip_bin)

        # Replace protected paths with the unprotected path
        paths = [path for path in paths if path not in protected_set]
        paths.insert(0, str(sip_bin))

    # Handle unsupported commands
    unsupported_bin = runner.make_temp("unsup_bin")
    unsupported = ["ping", "traceroute"]
    if replace_dns_tools:
        unsupported += ["nslookup", "dig", "host"]
    make_unsupported_tool(unsupported, unsupported_bin)
    paths.insert(0, str(unsupported_bin))

    return os.pathsep.join(paths)
