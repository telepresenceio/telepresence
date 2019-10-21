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

import json
import os
import ssl
import sys
from shutil import which
from subprocess import CalledProcessError
from typing import Tuple
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

from telepresence.runner import KubeInfo, Runner


def kubectl_or_oc(server: str) -> str:
    """
    Return "kubectl" or "oc", the command-line tool we should use.

    :param server: The URL of the cluster API server.
    """
    kubectl = "kubectl"
    oc = "oc"

    if which(oc) is None:
        return kubectl
    # We've got oc, and possibly kubectl as well. We only want oc for OpenShift
    # servers, so check for an OpenShift API endpoint exposing users
    # (it's also used by oc whoami command):
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        with urlopen(
            server + "/apis/user.openshift.io/v1/users/", context=ctx
        ) as response:
            api_group_list = str(response.read())
    except HTTPError as err:
        if err.code == 403:
            return oc
        else:
            return kubectl
    except URLError:
        return kubectl

    if "openshift" in api_group_list:
        return oc
    else:
        return kubectl


def _parse_version_component(comp: str) -> int:
    digits = []
    for ch in comp:
        if ch in "0123456789":
            digits.append(ch)
        else:
            break
    return int("".join(digits))  # or raise ValueError on empty string


def _parse_version(version: str) -> Tuple[int, int, int]:
    components = version.split(".", maxsplit=2)
    ints = [_parse_version_component(comp) for comp in components]
    major, minor, patch = ints  # or raise ValueError on number of items
    return major, minor, patch


def set_kube_command(runner: Runner, args) -> None:
    """Record the local machine Kubernetes configuration"""
    span = runner.span()

    # We don't quite know yet if we want kubectl or oc (if someone has both
    # it depends on the context), so until we know the context just guess.
    # We prefer kubectl over oc insofar as (1) kubectl commands we do in
    # this prelim setup stage don't require oc and (2) sometimes oc is a
    # different binary unrelated to OpenShift.
    missing = runner.depend(["kubectl", "oc"])
    if "kubectl" not in missing:
        prelim_command = "kubectl"
    elif "oc" not in missing:
        prelim_command = "oc"
    else:
        raise runner.fail("Found neither 'kubectl' nor 'oc' in your $PATH.")

    try:
        kubectl_version_output = runner.get_output([
            prelim_command, "version", "--short"
        ]).split("\n")
        command_version = kubectl_version_output[0].split(": v")[1]
        cluster_version = kubectl_version_output[1].split(": v")[1]
    except CalledProcessError as exc:
        ver = "(error: {})".format(exc)
        command_version = cluster_version = ver

    # Make sure we have a Kubernetes context set either on command line or
    # in kubeconfig:
    if args.context is None:
        try:
            args.context = runner.get_output([
                prelim_command, "config", "current-context"
            ])
        except CalledProcessError:
            sudo_used = ""
            if os.geteuid() == 0:
                sudo_used = "Sudo user detected. " + \
                    "We can't find a context " + \
                    "and maybe that's because we're running as root. " + \
                    "Try running without sudo."

            raise runner.fail(
                "No current-context set. "
                "Please use the --context option to explicitly set the "
                "context."
                "\n{}".format(sudo_used)
            )
    context = args.context

    # Figure out explicit namespace if its not specified, and the server
    # address (we use the server address to determine for good whether we
    # want oc or kubectl):
    kubectl_config = json.loads(
        runner.get_output([prelim_command, "config", "view", "-o", "json"])
    )
    for context_setting in kubectl_config["contexts"]:
        if context_setting["name"] == args.context:
            if args.namespace is None:
                args.namespace = context_setting["context"].get(
                    "namespace", "default"
                )
            cluster = context_setting["context"]["cluster"]
            break
    else:
        raise runner.fail("Error: Unable to find cluster information")

    # Check if the requested namespace exists
    try:
        runner.get_output([
            prelim_command, "--context", context, "get", "ns", args.namespace
        ]).split("\n")
        namespace = args.namespace
    except CalledProcessError:
        raise runner.fail(
            "Error: Namespace '{}' does not exist".format(args.namespace)
        )

    for cluster_setting in kubectl_config["clusters"]:
        if cluster_setting["name"] == cluster:
            server = cluster_setting["cluster"]["server"]
            break
    else:
        raise runner.fail("Error: Unable to find server information")

    command = kubectl_or_oc(server)

    runner.write("Command: {} {}".format(command, command_version))
    runner.write(
        "Context: {}, namespace: {}, version: {}\n".format(
            context, namespace, cluster_version
        )
    )
    in_local_vm = (
        args.local_cluster
        or _check_if_in_local_vm(runner, cluster, context, command, server)
    )
    if in_local_vm:
        runner.write("Looks like we're in a local VM, e.g. minikube.\n")

    kubeinfo = KubeInfo(
        cluster,
        cluster_version,
        command,
        command_version,
        server,
        context,
        namespace,
        in_local_vm,
        args.verbose,
    )
    runner.kubectl = kubeinfo

    _check_versions(runner)

    span.end()


def _check_if_in_local_vm(
    runner: Runner, cluster: str, context: str, command: str, server: str
) -> bool:
    local_context_names = {
        "docker-for-desktop",
        "docker-desktop",
        "minikube",
    }
    # Check by context name
    if context in local_context_names:
        return True
    # kind (kube-in-docker) has complex context name, so check by cluster
    if cluster == "kind":
        return True
    # Minishift has complex context name, so check by server:
    if command == "oc":
        try:
            ip = runner.get_output(["minishift", "ip"]).strip()
        except (OSError, CalledProcessError):
            return False
        if ip and ip in server:
            return True
    local_server_patterns = (
        "/localhost:",
        "/127.0.0.1:",
    )
    # Check by server address (e.g., https://localhost:6443)
    for pattern in local_server_patterns:
        if pattern in server:
            return True
    return False


def _check_versions(runner: Runner) -> None:
    k = runner.kubectl
    try:
        cluster = _parse_version(k.cluster_version)
    except ValueError:
        runner.write("Warning: Unable to parse cluster version number")
        return

    try:
        client = _parse_version(k.command_version)
    except ValueError:
        runner.write("Warning: Unable to parse client version number")
        return

    testing_message = "Warning: Telepresence has only been testing on "
    if cluster[0] != 1:
        runner.show(testing_message + "version 1.* clusters")
    if client[0] != 1:
        runner.show(testing_message + "kubectl version 1.*")

    warning_message = (
        "Warning: kubectl {} may not work correctly with cluster "
        "version {} due to the version discrepancy. See "
        "https://kubernetes.io/docs/setup/version-skew-policy/ "
        "for more information."
    ).format(k.command_version, k.cluster_version)

    major_is_diff = cluster[0] != client[0]
    minor_diff = abs(cluster[1] - client[1])
    if major_is_diff or minor_diff > 2:
        runner.show(warning_message)
        runner.show("\n")
    elif minor_diff > 1:
        runner.write(warning_message)


def final_checks(runner: Runner, args):
    """
    Perform some last cross-cutting checks
    """

    # Make sure we can access Kubernetes:
    try:
        runner.check_call(
            runner.kubectl(
                "get", "pods", "telepresence-connectivity-check",
                "--ignore-not-found"
            )
        )
    except CalledProcessError as exc:
        sys.stderr.write("Error accessing Kubernetes: {}\n".format(exc))
        if exc.stderr:
            sys.stderr.write("{}\n".format(exc.stderr.strip()))
        raise runner.fail("Cluster access failed")
    except (OSError, IOError) as exc:
        raise runner.fail(
            "Unexpected error accessing Kubernetes: {}\n".format(exc)
        )
