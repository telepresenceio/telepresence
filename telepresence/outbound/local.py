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
from subprocess import CalledProcessError, Popen
from typing import Dict, List

from telepresence.connect import SSH
from telepresence.proxy import RemoteInfo
from telepresence.runner import Runner
from telepresence.utilities import kill_process

from .vpn import connect_sshuttle
from .workarounds import apply_workarounds

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
    if runner.logfile_path != "-":
        torsocks_env["TORSOCKS_LOG_FILE_PATH"] = runner.logfile_path

    # Wait until DNS resolution via torsocks succeeds
    # FIXME: Make this lookup externally configurable
    # https://github.com/telepresenceio/telepresence/issues/389
    # https://github.com/telepresenceio/telepresence/issues/985
    test_hostname = "kubernetes.default"
    test_proxying_cmd = [
        "torsocks", "python3", "-c",
        "import socket; socket.socket().connect(('%s', 443))" % test_hostname
    ]
    launch_env = os.environ.copy()
    launch_env.update(torsocks_env)
    try:
        for _ in runner.loop_until(15, 0.1):
            try:
                runner.check_call(test_proxying_cmd, env=launch_env)
                return torsocks_env
            except CalledProcessError:
                pass
        raise RuntimeError("SOCKS network proxying failed to start...")
    finally:
        span.end()


def terminate_local_process(runner: Runner, process: Popen) -> None:
    ret = process.poll()
    if ret is None:
        runner.write("Killing local process...")
        kill_process(process)
    else:
        runner.write("Local process is already dead (ret={})".format(ret))


def launch_local(
    runner: Runner,
    command: List[str],
    env_overrides: Dict[str, str],
    replace_dns_tools: bool,
) -> Popen:
    # Compute user process environment
    env = os.environ.copy()
    env.update(env_overrides)
    env["PROMPT_COMMAND"] = (
        'PS1="@{}|$PS1";unset PROMPT_COMMAND'.format(runner.kubectl.context)
    )
    env["PATH"] = apply_workarounds(runner, env["PATH"], replace_dns_tools)

    # Launch the user process
    runner.show("Setup complete. Launching your command.")
    try:
        process = Popen(command, env=env)
    except OSError as exc:
        raise runner.fail("Failed to launch your command: {}".format(exc))
    runner.add_cleanup(
        "Terminate local process", terminate_local_process, runner, process
    )
    return process


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
    return launch_local(runner, command, env_overrides, True)


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
    _flush_dns_cache(runner)

    return launch_local(runner, command, env_overrides, False)


def _flush_dns_cache(runner: Runner):
    if runner.platform == "darwin":
        runner.show("Connected. Flushing DNS cache.")
        pkill_cmd = ["sudo", "-n", "/usr/bin/pkill", "-HUP", "mDNSResponder"]
        try:
            runner.check_call(pkill_cmd)
        except (OSError, CalledProcessError):
            pass
