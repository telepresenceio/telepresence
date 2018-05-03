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

import ssl
import sys

import json
from shutil import which
from subprocess import check_output, CalledProcessError, STDOUT
from types import SimpleNamespace
from typing import Optional
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

from telepresence.runner import Runner


def require_command(
    runner: Runner, command: str, message: Optional[str] = None
):
    if message is None:
        message = "Please install " + command
    try:
        runner.get_output(["which", command])
    except CalledProcessError as e:
        sys.stderr.write(message + "\n")
        sys.stderr.write(
            '(Ran "which {}" to check in your $PATH.)\n'.format(command)
        )
        sys.stderr.write(
            "See the documentation at https://telepresence.io "
            "for more details.\n"
        )
        raise SystemExit(1)


def kubectl_or_oc(server: str) -> str:
    """
    Return "kubectl" or "oc", the command-line tool we should use.

    :param server: The URL of the cluster API server.
    """
    if which("oc") is None:
        return "kubectl"
    # We've got oc, and possibly kubectl as well. We only want oc for OpenShift
    # servers, so check for an OpenShift API endpoint:
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        with urlopen(server + "/version/openshift", context=ctx) as u:
            u.read()
    except (URLError, HTTPError):
        return "kubectl"
    else:
        return "oc"


def analyze_kube(args):
    """Examine the local machine's kubernetes configuration"""
    res = SimpleNamespace()

    # We don't quite know yet if we want kubectl or oc (if someone has both
    # it depends on the context), so until we know the context just guess.
    # We prefer kubectl over oc insofar as (1) kubectl commands we do in
    # this prelim setup stage don't require oc and (2) sometimes oc is a
    # different binary unrelated to OpenShift.
    if which("kubectl"):
        prelim_command = "kubectl"
    elif which("oc"):
        prelim_command = "oc"
    else:
        raise SystemExit("Found neither 'kubectl' nor 'oc' in your $PATH.")

    try:
        kubectl_version_output = str(
            check_output([prelim_command, "version", "--short"]), "utf-8"
        ).split("\n")
        res.kubectl_version = kubectl_version_output[0].split(": v")[1]
        res.cluster_version = kubectl_version_output[1].split(": v")[1]
    except CalledProcessError as exc:
        res.kubectl_version = res.cluster_version = "(error: {})".format(exc)

    # Make sure we have a Kubernetes context set either on command line or
    # in kubeconfig:
    if args.context is None:
        try:
            args.context = str(
                check_output([prelim_command, "config", "current-context"],
                             stderr=STDOUT), "utf-8"
            ).strip()
        except CalledProcessError:
            raise SystemExit(
                "No current-context set. "
                "Please use the --context option to explicitly set the "
                "context."
            )

    # Figure out explicit namespace if its not specified, and the server
    # address (we use the server address to determine for good whether we
    # want oc or kubectl):
    kubectl_config = json.loads(
        str(
            check_output([prelim_command, "config", "view", "-o", "json"]),
            "utf-8"
        )
    )
    for context_setting in kubectl_config["contexts"]:
        if context_setting["name"] == args.context:
            if args.namespace is None:
                args.namespace = context_setting["context"].get(
                    "namespace", "default"
                )
            res.cluster = context_setting["context"]["cluster"]
            break
    else:
        raise SystemExit("Error: Unable to find cluster information")
    for cluster_setting in kubectl_config["clusters"]:
        if cluster_setting["name"] == res.cluster:
            res.server = cluster_setting["cluster"]["server"]
            break
    else:
        raise SystemExit("Error: Unable to find server information")

    res.command = kubectl_or_oc(res.server)

    return res
