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

from subprocess import DEVNULL, Popen
from urllib.request import urlopen

from telepresence.cli import crash_reporting
from telepresence.runner import wait_for_exit


def kill_intercept():
    try:
        with urlopen("http://teleproxy/api/shutdown", timeout=2.0) as fd:
            fd.read()
    except OSError:
        pass


def command(runner):
    with runner.cleanup_handling(), crash_reporting(runner):
        runner.require_sudo()
        runner.show("Setting up outbound connectivity...")
        runner.launch(
            "teleproxy intercept",
            ["sudo", "teleproxy", "-mode", "intercept"],
            killer=kill_intercept,
        )
        runner.launch(
            "teleproxy bridge",
            [
                "teleproxy", "-mode", "bridge", "-context",
                runner.kubectl.context, "-namespace", runner.kubectl.namespace
            ],
        )
        runner.show("Outbound is running. Press Ctrl-C/Ctrl-Break to quit.")
        user_process = Popen(["cat"], stdout=DEVNULL)
        wait_for_exit(runner, user_process)
