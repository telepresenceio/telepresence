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

import atexit
import signal

import sys
from types import SimpleNamespace

from telepresence.cli import parse_args, handle_unexpected_errors
from telepresence.container import run_docker_command
from telepresence.local import run_local_command
from telepresence.output import Output
from telepresence.proxy import start_proxy
from telepresence.mount import mount_remote
from telepresence.startup import analyze_args
from telepresence.usage_tracking import call_scout


def main():
    """
    Top-level function for Telepresence
    """

    session = SimpleNamespace()

    ########################################
    # Preliminaries: No changes to the machine or the cluster, no cleanup

    session.args = parse_args()  # tab-completion stuff goes here

    session.output = Output(session.args.logfile)
    del session.args.logfile

    session.kube_info, session.runner = analyze_args(session)

    span = session.runner.span()
    atexit.register(span.end)

    # Set up signal handling
    # Make SIGTERM and SIGHUP do clean shutdown (in particular, we want atexit
    # functions to be called):
    def shutdown(signum, frame):
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGHUP, shutdown)

    # Usage tracking
    call_scout(session)

    # Set up exit handling including crash reporter
    reporter = handle_unexpected_errors(session)
    # XXX exit handling via atexit

    ########################################
    # Now it's okay to change things

    @reporter
    def go():
        runner = session.runner
        args = session.args

        # Set up the proxy pod (operation -> pod name)
        # Connect to the proxy (pod name -> ssh object)
        # Capture remote environment information (ssh object -> env info)
        subprocesses, env, socks_port, ssh, remote_info = start_proxy(
            runner, args
        )

        # Used by mount_remote
        session.ssh = ssh
        session.remote_info = remote_info

        # Handle filesystem stuff (pod name, ssh object)
        mount_dir = mount_remote(session)

        # Set up outbound networking (pod name, ssh object)
        # Launch user command with the correct environment (...)
        if args.method == "container":
            run_docker_command(
                runner,
                remote_info,
                args,
                env,
                subprocesses,
                ssh,
                mount_dir,
            )
        else:
            run_local_command(
                runner, remote_info, args, env, subprocesses, socks_port, ssh,
                mount_dir
            )

        # Clean up (call the cleanup methods for everything above)
        # XXX handled by atexit

    go()


def run_telepresence():
    """Run telepresence"""
    if sys.version_info[:2] < (3, 5):
        raise SystemExit("Telepresence requires Python 3.5 or later.")
    main()


if __name__ == '__main__':
    run_telepresence()
