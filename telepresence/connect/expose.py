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

from typing import List, Tuple

from telepresence.runner import Runner
from telepresence.connect.ssh import SSH


def expose_local_services(
    runner: Runner, ssh: SSH, port_numbers: List[Tuple[int, int]]
) -> None:
    """Create SSH tunnels from remote proxy pod to local host.

    :param runner: The runner
    :param ssh: A 'SSH` instance.
    :param port_numbers: List of pairs of (local port, remote port).
    """
    if not port_numbers and runner.chatty:
        runner.show(
            "No traffic is being forwarded from the remote Deployment to your"
            " local machine. You can use the --expose option to specify which"
            " ports you want to forward."
        )
    remote_forward_arguments = []
    for local_port, remote_port in port_numbers:
        if runner.chatty:
            runner.show(
                "Forwarding remote port {} to local port {}.".format(
                    remote_port,
                    local_port,
                )
            )
        remote_forward_arguments.extend([
            "-R",
            "*:{}:127.0.0.1:{}".format(remote_port, local_port),
        ])
    if remote_forward_arguments:
        runner.launch(
            "SSH port forward (exposed ports)",
            ssh.bg_command(remote_forward_arguments)
        )
    if runner.chatty:
        runner.show("")
