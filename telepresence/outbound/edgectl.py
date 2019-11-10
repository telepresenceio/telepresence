# Copyright 2019 Datawire. All rights reserved.
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

import json
import subprocess
import typing

from telepresence.runner import Runner

ECResult = typing.Dict[str, str]


def run(runner: Runner, *args: str) -> ECResult:
    """
    Call Edge Control in batch mode. Accumulate the key/value results into a
    dictionary. Show error information on failure.
    """
    result = {}  # type: ECResult
    try:
        output = runner.get_output(["edgectl", "--batch"] + list(args))
    except subprocess.CalledProcessError as exc:
        runner.show("Call to Edge Control failed:")
        runner.report_subprocess_failure(exc)
        output = ""
    for line in output.splitlines():
        result.update(json.loads(line))  # JSONDecodeError or TypeError
    return result


def net_overrides_ok(status: ECResult) -> bool:
    return bool(status.get("net_overrides"))


def is_connected(status: ECResult) -> bool:
    """Return False if Edge Control is connecting or disconnected"""
    return bool(status.get("cluster.connected"))


def is_disconnected(status: ECResult) -> bool:
    """Return False if Edge Control is connecting or connected"""
    return "cluster.connected" not in status


def bridge_ok(status: ECResult) -> bool:
    return bool(status.get("bridge"))


def what_cluster(status: ECResult) -> typing.Tuple[str, str]:
    """
    Return the context and server to which Edge Control is connecting or
    connected
    """
    return status["cluster.context"], status["cluster.server"]


NET_OVERRIDES_MESSAGE = """
The Edge Control Daemon's network overrides are not ready. This may occur on
startup and when the network configuration changes. Please try again once
'edgectl status' does not report 'Network overrides NOT established'.
"""

WRONG_CLUSTER_MESSAGE = """
Edge Control is connected to a different cluster. Use 'edgectl disconnect' to
disconnect -- this will effect all processes -- then try again.
"""


def connect_teleproxy(runner: Runner) -> None:
    """Connect to Kubernetes using teleproxy via Edge Control."""
    span = runner.span()
    for _ in runner.loop_until(45, 0.5):
        status = run(runner, "status")
        if not status:
            raise runner.fail("Error: Failed to get status")

        if is_disconnected(status):
            # Try to connect
            connect_args = [
                "connect",
                "--context",
                runner.kubectl.context,
                "--namespace",
                runner.kubectl.namespace,
            ]
            connect = run(runner, *connect_args)
            if not connect:
                raise runner.fail("Error: Failed to connect")
            runner.add_cleanup(
                "edgectl disconnect",
                run,  # type: ignore
                runner,
                "disconnect",
            )
        else:  # Connecting or connected
            # Make sure we're talking to the right cluster
            connected_to = what_cluster(status)
            if connected_to != (runner.kubectl.context, runner.kubectl.server):
                runner.show(WRONG_CLUSTER_MESSAGE)
                runner.show("This is probably a bug...")
                raise runner.fail("Error: Connected to another cluster")

        if is_connected(status) and bridge_ok(status):
            break
    else:
        # runner.add_cleanup("Diagnose vpn-tcp", log_info_vpn_crash, runner)
        raise RuntimeError("Edge Control did not connect")
    span.end()


def is_running_quiet(runner: Runner) -> bool:
    """
    Determines whether the Edge Control Daemon is running without notifying the
    user that an edgectl command has failed. This is useful for vpn-tcp to
    check for a conflict between sshuttle and a running Edge Control Daemon.
    """
    # Check whether "edgectl status" succeeds
    try:
        runner.check_call(["edgectl", "status"], )
        return True
    except (OSError, subprocess.CalledProcessError):
        return False
