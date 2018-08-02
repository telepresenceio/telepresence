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

import argparse

from telepresence.proxy.deployment import create_new_deployment, \
    swap_deployment_openshift, supplant_deployment
from telepresence.proxy.remote import RemoteInfo, get_remote_info
from telepresence.runner import Runner


def start_proxy(runner: Runner, args: argparse.Namespace) -> RemoteInfo:
    """Start the kubectl port-forward and SSH clients that do the proxying."""
    span = runner.span()
    if runner.chatty and args.method != "container":
        runner.show(
            "Starting proxy with method '{}', which has the following "
            "limitations:".format(args.method)
        )
        if args.method == "vpn-tcp":
            runner.show(
                "All processes are affected, only one telepresence"
                " can run per machine, and you can't use other VPNs."
                " You may need to add cloud hosts with --also-proxy."
            )
        elif args.method == "inject-tcp":
            runner.show(
                "Go programs, static binaries, suid programs, and custom DNS"
                " implementations are not supported."
            )
        runner.show(
            "For a full list of method limitations see "
            "https://telepresence.io/reference/methods.html\n"
        )
    if args.mount and runner.chatty:
        runner.show(
            "\nVolumes are rooted at $TELEPRESENCE_ROOT. See "
            "https://telepresence.io/howto/volumes.html for details."
        )

    run_id = None

    if args.new_deployment is not None:
        # This implies --new-deployment:
        args.deployment, run_id = create_new_deployment(runner, args)

    if args.swap_deployment is not None:
        # This implies --swap-deployment
        if runner.kubectl.command == "oc":
            args.deployment, run_id, container_json = (
                swap_deployment_openshift(runner, args)
            )
        else:
            args.deployment, run_id, container_json = supplant_deployment(
                runner, args
            )
        args.expose.merge_automatic_ports([
            p["containerPort"] for p in container_json.get("ports", [])
            if p["protocol"] == "TCP"
        ])

    deployment_type = "deployment"
    if runner.kubectl.command == "oc":
        # OpenShift Origin uses DeploymentConfig instead, but for swapping we
        # mess with ReplicationController instead because mutating DC doesn't
        # work:
        if args.swap_deployment:
            deployment_type = "rc"
        else:
            deployment_type = "deploymentconfig"

    remote_info = get_remote_info(
        runner,
        args.deployment,
        args.context,
        args.namespace,
        deployment_type,
        run_id=run_id,
    )
    span.end()

    return remote_info
