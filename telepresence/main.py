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

from telepresence import connect, mount, outbound, proxy, remote_env, intercept
from telepresence.runner import Runner
from telepresence.cli import parse_args, crash_reporting
from telepresence.command_cli import parse_args as command_parse_args
from telepresence.startup import KubeInfo, final_checks
from telepresence.usage_tracking import call_scout


def main():
    """
    Top-level function for Telepresence
    """

    ########################################
    # Preliminaries: No changes to the machine or the cluster, no cleanup
    # Capture environment info and the user's intent

    # Check for a subcommand
    with crash_reporting():
        args = command_parse_args(None, only_for_commands=True)
    if args is not None:
        return command_main(args)

    with crash_reporting():
        args = parse_args()  # tab-completion stuff goes here

        runner = Runner(args.logfile, None, args.verbose)
        span = runner.span()
        runner.add_cleanup("Stop time tracking", span.end)
        runner.kubectl = KubeInfo(runner, args)

        start_proxy = proxy.setup(runner, args)
        do_connect = connect.setup(runner, args)
        get_remote_env, write_env_files = remote_env.setup(runner, args)
        launch = outbound.setup(runner, args)
        mount_remote = mount.setup(runner, args)

        final_checks(runner, args)

        # Usage tracking
        call_scout(runner, args)

    ########################################
    # Now it's okay to change things

    with runner.cleanup_handling(), crash_reporting(runner):
        # Set up the proxy pod (operation -> pod name)
        remote_info = start_proxy(runner)

        # Connect to the proxy (pod name -> ssh object)
        socks_port, ssh = do_connect(runner, remote_info)

        # Capture remote environment information (ssh object -> env info)
        env, pod_info = get_remote_env(runner, ssh, remote_info)

        # Handle filesystem stuff
        mount_dir = mount_remote(runner, env, ssh)

        # Maybe write environment files
        write_env_files(runner, env)

        # Set up outbound networking (pod name, ssh object)
        # Launch user command with the correct environment (...)
        user_process = launch(
            runner, remote_info, env, socks_port, ssh, mount_dir
        )

        runner.wait_for_exit(user_process)


def command_main(args):
    """
    Top-level function for Telepresence when executing subcommands
    """

    with crash_reporting():
        runner = Runner(args.logfile, None, args.verbose)
        span = runner.span()
        runner.add_cleanup("Stop time tracking", span.end)
        runner.kubectl = KubeInfo(runner, args)

        args.operation = args.command
        args.method = "teleproxy"
        call_scout(runner, args)

    if args.command == "outbound":
        return outbound.command(runner)

    if args.command == "intercept":
        return intercept.command(runner, args)

    raise runner.fail("Not implemented!")


def run_telepresence():
    """Run telepresence"""
    if sys.version_info[:2] < (3, 5):
        raise SystemExit("Telepresence requires Python 3.5 or later.")
    main()


if __name__ == '__main__':
    run_telepresence()
