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

from telepresence import (
    TELEPRESENCE_REMOTE_IMAGE, TELEPRESENCE_REMOTE_IMAGE_PRIV
)
from telepresence.proxy.deployment import (
    existing_deployment, create_new_deployment, swap_deployment_openshift,
    supplant_deployment
)
from telepresence.proxy.remote import RemoteInfo, get_remote_info
from telepresence.runner import Runner


def setup(runner: Runner, args):
    """
    Determine how the user wants to set up the proxy in the cluster.
    """
    deployment_type = "deployment"
    if runner.kubectl.command == "oc":
        # OpenShift Origin uses DeploymentConfig instead, but for swapping we
        # mess with ReplicationController instead because mutating DC doesn't
        # work:
        if args.swap_deployment:
            deployment_type = "rc"
        else:
            deployment_type = "deploymentconfig"

    if args.needs_root:
        image_name = TELEPRESENCE_REMOTE_IMAGE_PRIV
    else:
        image_name = TELEPRESENCE_REMOTE_IMAGE

    add_custom_nameserver = args.method == "vpn-tcp" and args.in_local_vm

    # Handle --deployment case
    deployment_arg = args.deployment
    operation = existing_deployment
    args.operation = "deployment"

    if args.new_deployment is not None:
        # This implies --new-deployment
        deployment_arg = args.new_deployment
        operation = create_new_deployment
        args.operation = "new_deployment"

    if args.swap_deployment is not None:
        # This implies --swap-deployment
        deployment_arg = args.swap_deployment
        if runner.kubectl.command == "oc":
            operation = swap_deployment_openshift
        else:
            operation = supplant_deployment
        args.operation = "swap_deployment"

    def start_proxy(runner_: Runner) -> RemoteInfo:
        tel_deployment, run_id = operation(
            runner_, deployment_arg, image_name, args.expose,
            add_custom_nameserver
        )
        remote_info = get_remote_info(
            runner,
            tel_deployment,
            deployment_type,
            run_id=run_id,
        )
        return remote_info

    return start_proxy
