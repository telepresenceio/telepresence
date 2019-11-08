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

from telepresence import connect, mount, outbound, proxy, remote_env
from telepresence.cli import crash_reporting, parse_args
from telepresence.runner import Runner
from telepresence.startup import final_checks, set_kube_command
from telepresence.usage_tracking import call_scout


def main():
    """
    Top-level function for Telepresence
    """

    with crash_reporting():
        ########################################
        # Preliminaries: No changes to the machine or the cluster, no cleanup
        # Capture environment info

        args = parse_args()  # tab-completion stuff goes here

        runner = Runner(args.logfile, args.verbose)
        span = runner.span()
        runner.add_cleanup("Stop time tracking", span.end)
        set_kube_command(runner, args)

    with runner.cleanup_handling(), crash_reporting(runner):
        ########################################
        # Intent: Fast, user prompts here, cleanup available
        # Capture the user's intent

        start_proxy = proxy.setup(runner, args)
        do_connect = connect.setup(runner, args)
        get_remote_env, write_env_files = remote_env.setup(runner, args)
        launch = outbound.setup(runner, args)
        mount_remote = mount.setup(runner, args)

        final_checks(runner, args)

        # Usage tracking
        call_scout(runner, args)

        ########################################
        # Action: Perform the user's intended operation(s)
        # Now it's okay to change things

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
            runner, remote_info, env, socks_port, ssh, mount_dir, pod_info
        )

        runner.wait_for_exit(user_process)


def run_telepresence():
    """Run telepresence"""
    if sys.version_info[:2] < (3, 5):
        raise SystemExit("Telepresence requires Python 3.5 or later.")
    main()


if __name__ == "__main__":
    run_telepresence()
