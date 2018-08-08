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
from subprocess import check_output, CalledProcessError, STDOUT, DEVNULL
from shutil import which
from typing import List
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

from telepresence.runner import Runner

# IP that shouldn't be in use on Internet, *or* local networks:
MAC_LOOPBACK_IP = "198.18.0.254"


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


class KubeInfo(object):
    """Record the local machine Kubernetes configuration"""

    def __init__(self, args):
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
            self.kubectl_version = kubectl_version_output[0].split(": v")[1]
            self.cluster_version = kubectl_version_output[1].split(": v")[1]
        except CalledProcessError as exc:
            ver = "(error: {})".format(exc)
            self.kubectl_version = self.cluster_version = ver

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
        self.context = args.context

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
                self.cluster = context_setting["context"]["cluster"]
                break
        else:
            raise SystemExit("Error: Unable to find cluster information")
        self.namespace = args.namespace

        for cluster_setting in kubectl_config["clusters"]:
            if cluster_setting["name"] == self.cluster:
                self.server = cluster_setting["cluster"]["server"]
                break
        else:
            raise SystemExit("Error: Unable to find server information")

        self.command = kubectl_or_oc(self.server)
        self.verbose = args.verbose

    def __call__(self, *in_args) -> List[str]:
        """Return command-line for running kubectl."""
        # Allow kubectl(arg1, arg2, arg3) or kubectl(*args) but also allow
        # kubectl(args) for convenience.
        if len(in_args) == 1 and type(in_args[0]) is not str:
            args = in_args[0]
        else:
            args = in_args
        result = [self.command]
        if self.verbose:
            result.append("--v=4")
        result.extend(["--context", self.context])
        result.extend(["--namespace", self.namespace])
        result += args
        return result


def analyze_args(output, args):
    """Construct session info based on user arguments"""
    kube_info = KubeInfo(args)
    output.write(
        "Context: {}, namespace: {}, kubectl_command: {}\n".format(
            kube_info.context, kube_info.namespace, kube_info.command
        )
    )

    runner = Runner(output, kube_info, args.verbose)

    # minikube/minishift break DNS because DNS gets captured, sent to
    # minikube, which sends it back to DNS server set by host, resulting in
    # loop... we've fixed that for most cases, but not --deployment.
    def check_if_in_local_vm() -> bool:
        # Minikube just has 'minikube' as context'
        if args.context == "minikube":
            return True
        # Minishift has complex context name, so check by server:
        if runner.kubectl.command == "oc" and which("minishift"):
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
        raise runner.fail(
            "vpn-tcp method doesn't work with minikube/minishift when"
            " using --deployment. Use --swap-deployment or"
            " --new-deployment instead."
        )

    # Make sure we can access Kubernetes:
    try:
        runner.get_output(
            runner.kubectl(
                "get", "pods", "telepresence-connectivity-check",
                "--ignore-not-found"
            ),
            stderr=STDOUT,
        )
    except (CalledProcessError, OSError, IOError) as exc:
        sys.stderr.write("Error accessing Kubernetes: {}\n".format(exc))
        if exc.output:
            sys.stderr.write("{}\n".format(exc.output.strip()))
        raise runner.fail("Cluster access failed")

    # Make sure we can run openssh:
    try:
        version = runner.get_output(["ssh", "-V"],
                                    stdin=DEVNULL,
                                    stderr=STDOUT)
        if not version.startswith("OpenSSH"):
            raise runner.fail("'ssh' is not the OpenSSH client, apparently.")
    except (CalledProcessError, OSError, IOError) as e:
        raise runner.fail("Error running ssh: {}\n".format(e))

    return kube_info, runner
