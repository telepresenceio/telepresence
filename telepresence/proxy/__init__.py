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
    swap_deployment, copy_deployment
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

    # Figure out if we need capability that allows for ports < 1024:
    image_name = TELEPRESENCE_REMOTE_IMAGE
    if any([p < 1024 for p in args.expose.remote()]):
        if runner.kubectl.command == "oc":
            # OpenShift doesn't support running as root:
            raise runner.fail("OpenShift does not support ports <1024.")
        image_name = TELEPRESENCE_REMOTE_IMAGE_PRIV

    # Figure out which operation the user wants
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
            operation = swap_deployment
        args.operation = "swap_deployment"

    if args.copy_deployment is not None:
        # This implies --copy-deployment
        deployment_arg = args.copy_deployment
        if runner.kubectl.command == "oc":
            # Todo:
            operation = swap_deployment_openshift
        else:
            operation = copy_deployment
        args.operation = "copy_deployment"
    # minikube/minishift break DNS because DNS gets captured, sent to minikube,
    # which sends it back to the DNS server set by host, resulting in a DNS
    # loop... We've fixed that for most cases by setting a distinct name server
    # for the proxy to use when making a new proxy pod, but that does not work
    # for --deployment.
    add_custom_ns = args.method == "vpn-tcp" and runner.kubectl.in_local_vm
    if add_custom_ns and args.operation == "deployment":
        raise runner.fail(
            "vpn-tcp method doesn't work with minikube/minishift when"
            " using --deployment. Use --swap-deployment or"
            " --new-deployment instead."
        )

    def start_proxy(runner_: Runner) -> RemoteInfo:
        tel_deployment, run_id = operation(
            runner_, deployment_arg, image_name, args.expose, add_custom_ns,
            args.forward_traffic
        )
        remote_info = get_remote_info(
            runner,
            tel_deployment,
            deployment_type,
            run_id=run_id,
        )
        return remote_info

    return start_proxy
