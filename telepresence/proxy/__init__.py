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
from subprocess import CalledProcessError

from .deployment import (
    existing_deployment, existing_deployment_openshift, create_new_deployment,
    swap_deployment_openshift, supplant_deployment
)
from .remote import RemoteInfo, get_remote_info
from telepresence.runner import Runner


def _dc_exists(runner: Runner, name: str) -> bool:
    """
    If we're using OpenShift Origin, we may be using a DeploymentConfig instead
    of a Deployment. Return True if a dc exists with the given name.
    """
    if runner.kubectl.command != "oc":
        return False
    try:
        runner.check_call(runner.kubectl("get", "dc/{}".format(name)))
        return True
    except CalledProcessError as exc:
        runner.show(
            "Failed to find OpenShift deploymentconfig {}:".format(name)
        )
        runner.show("  {}".format(str(exc.stderr)))
        runner.show("Will try regular Kubernetes Deployment.")
    return False


def setup(runner: Runner, args):
    """
    Determine how the user wants to set up the proxy in the cluster.
    """

    # OpenShift doesn't support running as root:
    if args.expose.has_privileged_ports() and runner.kubectl.command == "oc":
        raise runner.fail("OpenShift does not support ports <1024.")

    # Figure out which operation the user wants
    # Handle --deployment case
    deployment_arg = args.deployment
    if _dc_exists(runner, deployment_arg):
        operation = existing_deployment_openshift
        deployment_type = "deploymentconfig"
    else:
        operation = existing_deployment
        deployment_type = "deployment"
    args.operation = "deployment"

    if args.new_deployment is not None:
        # This implies --new-deployment
        deployment_arg = args.new_deployment
        operation = create_new_deployment
        deployment_type = "deployment"
        args.operation = "new_deployment"

    if args.swap_deployment is not None:
        # This implies --swap-deployment
        deployment_arg = args.swap_deployment
        if _dc_exists(runner, deployment_arg):
            operation = swap_deployment_openshift
            deployment_type = "deploymentconfig"
        else:
            operation = supplant_deployment
            deployment_type = "deployment"
        args.operation = "swap_deployment"

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
            runner_, deployment_arg, args.expose, add_custom_ns, args.service_account
        )
        remote_info = get_remote_info(
            runner,
            tel_deployment,
            deployment_type,
            run_id=run_id,
        )
        return remote_info

    return start_proxy
