import sys
from typing import List, Tuple

from telepresence.cleanup import Subprocesses
from telepresence.ssh import SSH


def expose_local_services(
    processes: Subprocesses, ssh: SSH, port_numbers: List[Tuple[int, int]]
) -> None:
    """Create SSH tunnels from remote proxy pod to local host.

    :param processes: A `Subprocesses` instance.
    :param ssh: A 'SSH` instance.
    :param port_numbers: List of pairs of (local port, remote port).
    """
    output = sys.stderr.isatty()
    if not port_numbers and output:
        print(
            "No traffic is being forwarded from the remote Deployment to your"
            " local machine. You can use the --expose option to specify which"
            " ports you want to forward.",
            file=sys.stderr
        )
    remote_forward_arguments = []
    for local_port, remote_port in port_numbers:
        if output:
            print(
                "Forwarding remote port {} to local port {}.".format(
                    remote_port,
                    local_port,
                ),
                file=sys.stderr
            )
        remote_forward_arguments.extend([
            "-R",
            "*:{}:127.0.0.1:{}".format(remote_port, local_port),
        ])
    if remote_forward_arguments:
        processes.append(ssh.popen(remote_forward_arguments))
    if output:
        print("", file=sys.stderr)
