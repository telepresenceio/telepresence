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
from tempfile import mkdtemp
from shutil import which
from subprocess import (CalledProcessError, STDOUT, DEVNULL)

from telepresence.cli import parse_args, handle_unexpected_errors
from telepresence.container import run_docker_command
from telepresence.local import run_local_command
from telepresence.output import Output
from telepresence.proxy import start_proxy
from telepresence.remote import mount_remote_volumes
from telepresence.runner import Runner
from telepresence.startup import analyze_kube, require_command
from telepresence.usage_tracking import call_scout


def main():
    """
    Top-level function for Telepresence
    """

    ########################################
    # Preliminaries: No changes to the machine or the cluster, no cleanup

    args = parse_args()  # tab-completion stuff goes here

    output = Output(args.logfile)
    args.logfile = output.logfile_path

    # Set up signal handling
    # Make SIGTERM and SIGHUP do clean shutdown (in particular, we want atexit
    # functions to be called):
    def shutdown(signum, frame):
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGHUP, shutdown)

    kube_info = analyze_kube(args)

    if args.deployment:
        operation = "deployment"
    elif args.new_deployment:
        operation = "new_deployment"
    elif args.swap_deployment:
        operation = "swap_deployment"
    else:
        operation = "bad_args"

    # Figure out if we need capability that allows for ports < 1024:
    if any([p < 1024 for p in args.expose.remote()]):
        if kube_info.command == "oc":
            # OpenShift doesn't support running as root:
            raise SystemExit("OpenShift does not support ports <1024.")
        args.needs_root = True
    else:
        args.needs_root = False

    # Usage tracking
    scouted = call_scout(
        kube_info.kubectl_version, kube_info.cluster_version, operation,
        args.method
    )

    runner = Runner(output, kube_info.command, args.verbose)
    span = runner.span()
    atexit.register(span.end)
    output.write("Scout info: {}\n".format(scouted))
    output.write(
        "Context: {}, namespace: {}, kubectl_command: {}\n".format(
            args.context, args.namespace, runner.kubectl_cmd
        )
    )

    # minikube/minishift break DNS because DNS gets captured, sent to
    # minikube, which sends it back to DNS server set by host, resulting in
    # loop... we've fixed that for most cases, but not --deployment.
    def check_if_in_local_vm() -> bool:
        # Minikube just has 'minikube' as context'
        if args.context == "minikube":
            return True
        # Minishift has complex context name, so check by server:
        if runner.kubectl_cmd == "oc" and which("minishift"):
            ip = runner.get_output(["minishift", "ip"]).strip()
            if ip and ip in kube_info.server:
                return True
        return False

    args.in_local_vm = check_if_in_local_vm()
    if args.in_local_vm:
        output.write("Looks like we're in a local VM, e.g. minikube.\n")
    if (
        args.in_local_vm and args.method == "vpn-tcp"
        and args.new_deployment is None and args.swap_deployment is None
    ):
        raise SystemExit(
            "vpn-tcp method doesn't work with minikube/minishift when"
            " using --deployment. Use --swap-deployment or"
            " --new-deployment instead."
        )

    # Make sure we can access Kubernetes:
    try:
        runner.get_kubectl(
            args.context,
            args.namespace, [
                "get", "pods", "telepresence-connectivity-check",
                "--ignore-not-found"
            ],
            stderr=STDOUT
        )
    except (CalledProcessError, OSError, IOError) as exc:
        sys.stderr.write("Error accessing Kubernetes: {}\n".format(exc))
        if exc.output:
            sys.stderr.write("{}\n".format(exc.output.strip()))
        raise SystemExit(1)

    # Make sure we can run openssh:
    try:
        version = runner.get_output(["ssh", "-V"],
                                    stdin=DEVNULL,
                                    stderr=STDOUT)
        if not version.startswith("OpenSSH"):
            raise SystemExit("'ssh' is not the OpenSSH client, apparently.")
    except (CalledProcessError, OSError, IOError) as e:
        sys.stderr.write("Error running ssh: {}\n".format(e))
        raise SystemExit(1)

    # Other requirements:
    require_command(
        runner, "torsocks", "Please install torsocks (v2.1 or later)"
    )
    if args.mount:
        require_command(runner, "sshfs")

    # Need conntrack for sshuttle on Linux:
    if sys.platform.startswith("linux") and args.method == "vpn-tcp":
        require_command(runner, "conntrack")

    # Set up exit handling including crash reporter
    reporter = handle_unexpected_errors(args.logfile, runner)
    # XXX exit handling via atexit

    ########################################
    # Now it's okay to change things

    @reporter
    def go():
        # Set up the proxy pod (operation -> pod name)
        # Connect to the proxy (pod name -> ssh object)
        # Capture remote environment information (ssh object -> env info)
        subprocesses, env, socks_port, ssh, remote_info = start_proxy(
            runner, args
        )

        # Handle filesystem stuff (pod name, ssh object)
        if args.mount:
            # The mount directory is made here, removed by mount_cleanup if
            # mount succeeds, leaked if mount fails.
            if args.mount is True:
                # Docker for Mac only shares some folders; the default TMPDIR
                # on OS X is not one of them, so make sure we use /tmp:
                mount_dir = mkdtemp(dir="/tmp")
            else:
                # FIXME: Maybe warn if args.mount doesn't start with /tmp?
                try:
                    args.mount.mkdir(parents=True, exist_ok=True)
                except OSError as exc:
                    exit("Unable to use mount path: {}".format(exc))
                mount_dir = str(args.mount)
            # We allow all users if we're using Docker because we don't know
            # what uid the Docker container will use.
            mount_dir, mount_cleanup = mount_remote_volumes(
                runner,
                remote_info,
                ssh,
                args.method == "container",  # allow all users
                mount_dir,
            )
            atexit.register(mount_cleanup)
        else:
            mount_dir = None

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
