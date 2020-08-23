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

from subprocess import CalledProcessError, TimeoutExpired
from typing import List

from telepresence.connect import SSH
from telepresence.outbound.cidr import get_proxy_cidrs
from telepresence.proxy import RemoteInfo
from telepresence.runner import Runner


def get_sshuttle_command(ssh: SSH, method: str = "auto") -> List[str]:
    return [
        "sshuttle-telepresence",
        "-v",
        "--dns",
        "--method",
        method,
        "-e",
        "ssh {}".format(" ".join(ssh.required_args)),
        "-r",
        "{}:{}".format(ssh.user_at_host, ssh.port),
    ]


def dns_lookup(runner: Runner, name: str, timeout: int) -> bool:
    """
    Performs the requested DNS lookup and returns success or failure
    """
    code = "import socket; socket.gethostbyname(\"{}\")".format(name)
    try:
        # Do the DNS lookup in a subprocess, as this is an easy way to put a
        # timeout on DNS. But discard the output, as it's just noise in the
        # logs anyhow. Use get_output to avoid logging.
        runner.get_output(
            ["python3", "-c", code],
            stderr_to_stdout=True,
            timeout=timeout,
        )
        return True
    except (CalledProcessError, TimeoutExpired):
        pass
    return False


def connect_sshuttle(
    runner: Runner, remote_info: RemoteInfo, hosts_or_ips: List[str], ssh: SSH
) -> None:
    """Connect to Kubernetes using sshuttle."""
    span = runner.span()
    sshuttle_method = "auto"
    if runner.platform == "linux":
        # sshuttle tproxy mode seems to have issues:
        sshuttle_method = "nat"
    runner.launch(
        "sshuttle",
        get_sshuttle_command(ssh, sshuttle_method) + [
            # DNS proxy running on remote pod:
            "--to-ns",
            "127.0.0.1:9053",
        ] + get_proxy_cidrs(runner, remote_info, hosts_or_ips),
        keep_session=True,  # Avoid trouble with interactive sudo
    )

    # sshuttle will take a while to startup. We can detect it being up when
    # DNS resolution of services starts working. We use a specific single
    # segment so any search/domain statements in resolv.conf are applied,
    # which then allows the DNS proxy to detect the suffix domain and
    # filter it out.
    # On Macs, and perhaps elsewhere, there is OS-level caching of
    # NXDOMAIN, so bypass caching by sending new domain each time. Another,
    # less robust alternative, is to `killall -HUP mDNSResponder`.
    subspan = runner.span("sshuttle-wait")
    countdown = 3
    for idx in runner.loop_until(35, 0.1):
        # Construct a different name each time to avoid NXDOMAIN caching.
        name = "hellotelepresence-{}".format(idx)
        runner.write("Wait for vpn-tcp connection: {}".format(name))
        if dns_lookup(runner, name, 5):
            countdown -= 1
            runner.write("Resolved {}. {} more...".format(name, countdown))
            if countdown == 0:
                break
        # The loop uses a single segment to try to capture suffix or search
        # path in the proxy. However, in some network setups, single-segment
        # names don't get resolved the normal way. To see whether we're running
        # into this, also try to resolve a name with many dots. This won't
        # resolve successfully but will show up in the logs. See also:
        # https://github.com/telepresenceio/telepresence/issues/242. We use a
        # short timeout here because (1) this takes a long time for some users
        # and (2) we're only looking for a log entry; we don't expect this to
        # succeed and don't benefit from waiting for the NXDOMAIN.
        many_dotted_name = "{}.a.sanity.check.telepresence.io".format(name)
        dns_lookup(runner, many_dotted_name, 1)

    if countdown != 0:
        runner.add_cleanup("Diagnose vpn-tcp", log_info_vpn_crash, runner)
        raise RuntimeError("vpn-tcp tunnel did not connect")

    subspan.end()
    span.end()


def log_info_vpn_crash(runner: Runner) -> None:
    """
    Log some stuff that may help diagnose vpn-tcp method failures.
    """
    commands = [
        "ls -l /etc/resolv.conf",
        "grep -v ^# /etc/resolv.conf",
        "ls -l /etc/resolvconf",
        "cat /etc/nsswitch.conf",
        "ls -l /etc/resolver",
    ]
    for command in commands:
        try:
            runner.check_call(command.split(), timeout=1)
        except (CalledProcessError, TimeoutExpired):
            pass
