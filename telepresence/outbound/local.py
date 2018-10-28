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
from subprocess import CalledProcessError, Popen
from typing import Dict, List

from telepresence.outbound.workarounds import apply_workarounds
from telepresence.utilities import kill_process
from telepresence.proxy.remote import RemoteInfo
from telepresence.runner import Runner
from telepresence.connect.ssh import SSH
from telepresence.outbound.vpn import connect_sshuttle

TORSOCKS_CONFIG = """
# Allow process to listen on ports:
AllowInbound 1
# Allow process to connect to localhost:
AllowOutboundLocalhost 1
# Connect to custom port for SOCKS server:
TorPort {}
"""


def set_up_torsocks(runner: Runner, socks_port: int) -> Dict[str, str]:
    """
    Set up environment variables and configuration to make torsocks work
    correctly. Wait for connectivity.
    """
    span = runner.span()
    # Create custom torsocks.conf, since some options we want (in particular,
    # port) aren't accessible via env variables in older versions of torsocks:
    tor_conffile = runner.temp / "tel_torsocks.conf"
    tor_conffile.write_text(TORSOCKS_CONFIG.format(socks_port))

    torsocks_env = dict()
    torsocks_env["TORSOCKS_CONF_FILE"] = str(tor_conffile)
    if runner.output.logfile is not sys.stdout:
        torsocks_env["TORSOCKS_LOG_FILE_PATH"] = runner.output.logfile.name

    # Wait until DNS resolution via torsocks succeeds
    # FIXME: Make this lookup for google.com configurable
    # https://github.com/telepresenceio/telepresence/issues/389
    test_proxying_cmd = [
        "torsocks", "python3", "-c",
        "import socket; socket.socket().connect(('google.com', 80))"
    ]
    launch_env = os.environ.copy()
    launch_env.update(torsocks_env)
    try:
        for _ in runner.loop_until(10, 0.1):
            try:
                runner.check_call(test_proxying_cmd, env=launch_env)
                return torsocks_env
            except CalledProcessError:
                pass
        raise RuntimeError("SOCKS network proxying failed to start...")
    finally:
        span.end()


def terminate_local_process(runner, process):
    if process.poll() is None:
        runner.write("Killing local process...")
        kill_process(process)


def get_local_env(runner, env_overrides, replace_dns_tools):
    env = os.environ.copy()
    env.update(env_overrides)
    env["PROMPT_COMMAND"] = (
        'PS1="@{}|$PS1";unset PROMPT_COMMAND'.format(runner.kubectl.context)
    )
    env["PATH"] = apply_workarounds(runner, env["PATH"], replace_dns_tools)
    return env


def launch_inject(
    runner: Runner,
    command: List[str],
    socks_port: int,
    env_overrides: Dict[str, str],
) -> Popen:
    """
    Launch the user's command under torsocks
    """
    torsocks_env = set_up_torsocks(runner, socks_port)
    env_overrides.update(torsocks_env)
    env = get_local_env(runner, env_overrides, True)
    process = Popen(command, env=env)
    runner.add_cleanup(
        "Terminate local process", terminate_local_process, runner, process
    )
    return process


def launch_vpn(
    runner: Runner,
    remote_info: RemoteInfo,
    command: List[str],
    also_proxy: List[str],
    env_overrides: Dict[str, str],
    ssh: SSH,
) -> Popen:
    """
    Launch sshuttle and the user's command
    """
    connect_sshuttle(runner, remote_info, also_proxy, ssh)
    env = get_local_env(runner, env_overrides, False)
    process = Popen(command, env=env)
    runner.add_cleanup(
        "Terminate local process", terminate_local_process, runner, process
    )
    return process


def launch_none(
        runner: Runner,
        command: List[str],
        env_overrides: Dict[str, str]):
    env = get_local_env(runner, env_overrides, False)
    process = Popen(command, env=env)
    runner.add_cleanup(
        "Terminate local process", terminate_local_process, runner, process
    )
    return process
