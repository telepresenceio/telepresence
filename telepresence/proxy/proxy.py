# Copyright 2020 Datawire. All rights reserved.
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
import os
import re
from subprocess import CalledProcessError
from typing import Callable, Dict

from telepresence.runner import Runner
from telepresence.utilities import get_alternate_nameserver

from .deployment import (
    create_new_deployment, existing_deployment, existing_deployment_openshift,
    supplant_deployment, swap_deployment_openshift
)
from .operation import ProxyIntent, Legacy
from .remote import RemoteInfo, get_remote_info


def _dc_exists(runner: Runner, name: str) -> bool:
    """
    If we're using OpenShift Origin, we may be using a DeploymentConfig instead
    of a Deployment. Return True if a dc exists with the given name.
    """
    # Need to use oc to manage DeploymentConfigs. The cluster needs to be
    # running OpenShift as well. Check for both.
    kube = runner.kubectl
    if kube.command != "oc" or not kube.cluster_is_openshift:
        return False
    if ":" in name:
        name, container = name.split(":", 1)
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


def setup(runner: Runner,
          args: argparse.Namespace) -> Callable[[Runner], RemoteInfo]:
    """
    Determine how the user wants to set up the proxy in the cluster.
    """

    if os.environ.get("TELEPRESENCE_USE_DEPLOYMENT", ""):
        return legacy_setup(runner, args)

    runner.show(
        "Using a Pod instead of a Deployment for the Telepresence proxy. "
        "If you experience problems, please file an issue!"
    )
    runner.show(
        "Set the environment variable TELEPRESENCE_USE_DEPLOYMENT to any "
        "non-empty value to force the old behavior, e.g.,"
    )
    runner.show(
        "    env TELEPRESENCE_USE_DEPLOYMENT=1 telepresence --run curl hello"
    )
    runner.show("\n")

    # OpenShift doesn't support running as root:
    if (
        args.expose.has_privileged_ports()
        and runner.kubectl.cluster_is_openshift
    ):
        raise runner.fail("OpenShift does not support ports <1024.")

    # Check the service account, if present
    if args.service_account:
        try:
            runner.check_call(
                runner.kubectl("get", "serviceaccount", args.service_account)
            )
        except CalledProcessError as exc:
            raise runner.fail(
                "Check service account {} failed:\n{}".format(
                    args.service_account, exc.stderr
                )
            )

    # Collect user intent
    name, container = args.deployment_arg, ""
    if ":" in name:
        name, container = name.split(":", 1)
    deployment_env = {}  # type: Dict[str, str]

    # minikube/minishift break DNS because DNS gets captured, sent to minikube,
    # which sends it back to the DNS server set by host, resulting in a DNS
    # loop... We've fixed that for most cases by setting a distinct name server
    # for the proxy to use when making a new proxy pod, but that does not work
    # for --deployment.
    if args.method == "vpn-tcp" and runner.kubectl.in_local_vm:
        if args.operation == "deployment":
            raise runner.fail(
                "vpn-tcp method doesn't work with minikube/minishift when"
                " using --deployment. Use --swap-deployment or"
                " --new-deployment instead."
            )
        try:
            deployment_env["TELEPRESENCE_NAMESERVER"] \
                = get_alternate_nameserver()
            if args.also_proxy:
                proxy_names = []
                for name in args.also_proxy:
                    if not (
                        re.search(r"[^\w.]", name)
                        or re.match(r"^(?:\d+\.){3}\d+$", name)
                    ):
                        proxy_names.append(name)
                if proxy_names:
                    deployment_env["TELEPRESENCE_LOCAL_NAMES"] \
                        = ",".join(proxy_names)

        except Exception as exc:
            raise runner.fail(
                "Failed to find a fallback nameserver: {}".format(exc)
            )

    intent = ProxyIntent(
        name,
        container,
        args.expose,
        deployment_env,
        args.service_account or "",
    )

    # Figure out which operation the user wants
    if args.operation == "deployment":
        if _dc_exists(runner, args.deployment_arg):
            operation = Legacy(intent, existing_deployment_openshift)
        else:
            operation = Legacy(intent, existing_deployment)
    elif args.operation == "new_deployment":
        operation = Legacy(intent, create_new_deployment)
    else:
        assert args.operation == "swap_deployment"
        if _dc_exists(runner, args.deployment_arg):
            operation = Legacy(intent, swap_deployment_openshift)
        else:
            operation = Legacy(intent, supplant_deployment)

    operation.prepare(runner)

    return operation.act


def legacy_setup(runner: Runner, args):
    """
    Determine how the user wants to set up the proxy in the cluster.
    """

    # OpenShift doesn't support running as root:
    if (
        args.expose.has_privileged_ports()
        and runner.kubectl.cluster_is_openshift
    ):
        raise runner.fail("OpenShift does not support ports <1024.")

    # Check the service account, if present
    if args.service_account:
        try:
            runner.check_call(
                runner.kubectl("get", "serviceaccount", args.service_account)
            )
        except CalledProcessError as exc:
            raise runner.fail(
                "Check service account {} failed:\n{}".format(
                    args.service_account, exc.stderr
                )
            )

    # Figure out which operation the user wants
    if args.deployment is not None:
        # This implies --deployment
        if _dc_exists(runner, args.deployment_arg):
            operation = existing_deployment_openshift
            deployment_type = "deploymentconfig"
        else:
            operation = existing_deployment
            deployment_type = "deployment"

    if args.new_deployment is not None:
        # This implies --new-deployment
        deployment_type = "deployment"
        operation = create_new_deployment

    if args.swap_deployment is not None:
        # This implies --swap-deployment
        if _dc_exists(runner, args.deployment_arg):
            operation = swap_deployment_openshift
            deployment_type = "deploymentconfig"
        else:
            operation = supplant_deployment
            deployment_type = "deployment"

    # minikube/minishift break DNS because DNS gets captured, sent to minikube,
    # which sends it back to the DNS server set by host, resulting in a DNS
    # loop... We've fixed that for most cases by setting a distinct name server
    # for the proxy to use when making a new proxy pod, but that does not work
    # for --deployment.
    deployment_env = {}
    if args.method == "vpn-tcp" and runner.kubectl.in_local_vm:
        if args.operation == "deployment":
            raise runner.fail(
                "vpn-tcp method doesn't work with minikube/minishift when"
                " using --deployment. Use --swap-deployment or"
                " --new-deployment instead."
            )
        try:
            deployment_env["TELEPRESENCE_NAMESERVER"] \
                = get_alternate_nameserver()
            if args.also_proxy:
                proxy_names = []
                for name in args.also_proxy:
                    if not (
                        re.search(r"[^\w.]", name)
                        or re.match(r"^(?:\d+\.){3}\d+$", name)
                    ):
                        proxy_names.append(name)
                if proxy_names:
                    deployment_env["TELEPRESENCE_LOCAL_NAMES"] \
                        = ",".join(proxy_names)

        except Exception as exc:
            raise runner.fail(
                "Failed to find a fallback nameserver: {}".format(exc)
            )

    def start_proxy(runner_: Runner) -> RemoteInfo:
        tel_deployment, run_id = operation(
            runner_, args.deployment_arg, args.expose, deployment_env,
            args.service_account
        )
        remote_info = get_remote_info(
            runner,
            tel_deployment,
            deployment_type,
            run_id=run_id,
        )
        return remote_info

    return start_proxy
