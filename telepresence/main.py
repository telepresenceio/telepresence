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
"""
Telepresence: local development environment for a remote Kubernetes cluster.
"""

import sys
from types import SimpleNamespace

from telepresence import connect, mount, outbound, proxy
from telepresence.runner import wait_for_exit
from telepresence.cli import parse_args, crash_reporting
from telepresence.container import run_docker_command
from telepresence.local import run_local_command
from telepresence.output import Output
from telepresence.remote_env import get_remote_env, write_env_files
from telepresence.startup import analyze_args
from telepresence.usage_tracking import call_scout


def main(session):
    """
    Top-level function for Telepresence
    """

    ########################################
    # Preliminaries: No changes to the machine or the cluster, no cleanup

    with crash_reporting():
        session.args = parse_args()  # tab-completion stuff goes here

        session.output = Output(session.args.logfile)
        del session.args.logfile

        session.kube_info, session.runner = analyze_args(session)

        span = session.runner.span()
        session.runner.add_cleanup("Stop time tracking", span.end)

        runner = session.runner
        args = session.args
        start_proxy = proxy.setup(runner, args)
        do_connect = connect.setup(runner, args)
        mount_remote = mount.setup(runner, args)
        outbound.setup(runner, args)

        # Usage tracking
        call_scout(session)

    ########################################
    # Now it's okay to change things

    with session.runner.cleanup_handling(), crash_reporting(session.runner):
        runner = session.runner
        args = session.args

        # Set up the proxy pod (operation -> pod name)
        remote_info = start_proxy(runner)

        # Connect to the proxy (pod name -> ssh object)
        socks_port, ssh = do_connect(runner, remote_info)

        # Capture remote environment information (ssh object -> env info)
        env = get_remote_env(runner, args, remote_info)

        # Used by mount_remote
        session.ssh = ssh
        session.remote_info = remote_info
        session.env = env

        # Handle filesystem stuff
        mount_dir = mount_remote(runner, env, ssh)

        # Maybe write environment files
        write_env_files(session)

        # Set up outbound networking (pod name, ssh object)
        # Launch user command with the correct environment (...)
        if args.method == "container":
            user_process = run_docker_command(
                runner,
                remote_info,
                args,
                env,
                ssh,
                mount_dir,
            )
        else:
            user_process = run_local_command(
                runner, remote_info, args, env, socks_port, ssh
            )

        wait_for_exit(runner, user_process)


def run_telepresence():
    """Run telepresence"""
    if sys.version_info[:2] < (3, 5):
        raise SystemExit("Telepresence requires Python 3.5 or later.")

    session = SimpleNamespace()
    main(session)


if __name__ == '__main__':
    run_telepresence()
