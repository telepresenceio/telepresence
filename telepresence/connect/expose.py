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

from .ssh import SSH


def expose_local_services(
    runner: Runner,
    ssh: SSH,
    exp_port_numbers: List[Tuple[int, int]],
    to_pod: List[int],
    from_pod: List[int],
    show_only: bool = False,
) -> None:
    """Create SSH tunnels from remote proxy pod to local host.

    The show_only param is used to show messages for the container method; the
    tunnels are created in the network container, where those messages are not
    visible to the user.
    """
    if not exp_port_numbers and runner.chatty:
        runner.show(
            "\nNo traffic is being forwarded from the remote Deployment to "
            "your local machine. You can use the --expose option to specify "
            "which ports you want to forward."
        )
    forward_arguments = []  # type: List[str]
    for local_port, remote_port in exp_port_numbers:
        if runner.chatty:
            runner.show(
                "Forwarding remote port {} to local port {}.".format(
                    remote_port,
                    local_port,
                )
            )
        forward_arguments.extend([
            "-R",
            "*:{}:127.0.0.1:{}".format(remote_port, local_port),
        ])
    for port in to_pod:
        if runner.chatty:
            runner.show("Forwarding localhost:{} to the pod".format(port))
        forward_arguments.extend([
            "-L",
            "127.0.0.1:{}:127.0.0.1:{}".format(port, port),
        ])
    for port in from_pod:
        if runner.chatty:
            runner.show("Forwarding localhost:{} from the pod".format(port))
        forward_arguments.extend([
            "-R",
            "127.0.0.1:{}:127.0.0.1:{}".format(port, port),
        ])
    if forward_arguments and not show_only:
        runner.launch(
            "SSH port forward (exposed ports)",
            ssh.bg_command(forward_arguments)
        )
    if runner.chatty:
        runner.show("\n")
