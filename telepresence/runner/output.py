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
import sys
import typing
from collections import deque
from time import ctime
from time import time as curtime

from telepresence import __version__, image_version, version_override
from telepresence.utilities import str_command


def _open_logfile(logfile_path: str) -> typing.TextIO:
    """
    Try to open the specified path for the logfile.
    :param logfile_path: as it says
    :return: open file handle
    """
    # Wipe existing logfile and notice problems
    try:
        open(logfile_path, "w").close()
    except OSError as exc:
        exit("Failed to open logfile ({}): {}".format(logfile_path, exc))

    # Open using append mode so multiple processes don't clobber each other's
    # outputs, and use line buffering so data gets written out immediately.
    return open(logfile_path, "a", buffering=1)


class Output:
    """Logging and display"""
    def __init__(self, logfile_path: str) -> None:
        """
        Create output handle

        :param logfile_path: Path or string file path or "-" for stdout
        """

        # Fail if current working directory does not exist so we don't crash in
        # standard library path-handling code.
        try:
            os.getcwd()
        except OSError:
            exit("T: Error: Current working directory does not exist.")

        if logfile_path == "-":
            self.logfile = sys.stdout
        else:
            # Log file path should be absolute since some processes may run in
            # different directories:
            logfile_path = os.path.abspath(logfile_path)
            self.logfile = _open_logfile(logfile_path)
        self.logfile_path = logfile_path

        self.start_time = curtime()

        # keep last 25 lines
        self.logtail = deque(maxlen=25)  # type: typing.Deque[str]

        self.write(
            "Telepresence {} launched at {}".format(__version__, ctime())
        )
        self.write("  {}".format(str_command(sys.argv)))
        if version_override:
            self.write("  TELEPRESENCE_VERSION is {}".format(image_version))
        elif image_version != __version__:
            self.write("  Using images version {} (dev)".format(image_version))

    def write(self, message: str, prefix: str = "TEL") -> None:
        """Write a message to the log."""
        if self.logfile.closed:
            return
        for sub_message in message.splitlines():
            line = "{:6.1f} {} | {}\n".format(
                curtime() - self.start_time, prefix, sub_message.rstrip()
            )
            self.logfile.write(line)
            self.logtail.append(line)
        self.logfile.flush()

    def read_logs(self) -> str:
        """Return the end of the contents of the log"""
        return "".join(self.logtail)
