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
import json
from subprocess import STDOUT
from typing import Tuple, Dict
from uuid import uuid4

from copy import deepcopy

from telepresence import (
    TELEPRESENCE_REMOTE_IMAGE, TELEPRESENCE_REMOTE_IMAGE_PRIV
)
from telepresence.remote import get_deployment_json
from telepresence.runner import Runner
from telepresence.utilities import get_alternate_nameserver


def create_new_deployment(runner: Runner,
                          args: argparse.Namespace) -> Tuple[str, str]:
    """Create a new Deployment, return its name and Kubernetes label."""
    span = runner.span()
    run_id = str(uuid4())

    def remove_existing_deployment():
        runner.get_output(
            runner.kubectl(
                "delete",
                "--ignore-not-found",
                "svc,deploy",
                "--selector=telepresence=" + run_id,
            )
        )

    runner.add_cleanup("Delete new deployment", remove_existing_deployment)
    remove_existing_deployment()
    if args.needs_root:
        image_name = TELEPRESENCE_REMOTE_IMAGE_PRIV
    else:
        image_name = TELEPRESENCE_REMOTE_IMAGE
    command = [
        "run",
        # This will result in using Deployment:
        "--restart=Always",
        "--limits=cpu=100m,memory=256Mi",
        "--requests=cpu=25m,memory=64Mi",
        args.new_deployment,
        "--image=" + image_name,
        "--labels=telepresence=" + run_id,
    ]
    # Provide a stable argument ordering.  Reverse it because that happens to
    # make some current tests happy but in the long run that's totally
    # arbitrary and doesn't need to be maintained.  See issue 494.
    for port in sorted(args.expose.remote(), reverse=True):
        command.append("--port={}".format(port))
    if args.expose.remote():
        command.append("--expose")
    # If we're on local VM we need to use different nameserver to prevent
    # infinite loops caused by sshuttle:
    if args.method == "vpn-tcp" and args.in_local_vm:
        command.append(
            "--env=TELEPRESENCE_NAMESERVER=" + get_alternate_nameserver()
        )
    runner.get_output(runner.kubectl(command))
    span.end()
    return args.new_deployment, run_id


def supplant_deployment(runner: Runner,
                        args: argparse.Namespace) -> Tuple[str, str, Dict]:
    """
    Swap out an existing Deployment, supplant method.

    Native Kubernetes version.

    Returns (Deployment name, unique K8s label, JSON of original container that
    was swapped out.)
    """
    span = runner.span()
    run_id = str(uuid4())

    deployment_name, *container_name = args.swap_deployment.split(":", 1)
    if container_name:
        container_name = container_name[0]
    deployment_json = get_deployment_json(
        runner,
        deployment_name,
        args.context,
        args.namespace,
        "deployment",
    )

    # If no container name was given, just use the first one:
    if not container_name:
        container_name = deployment_json["spec"]["template"]["spec"][
            "containers"
        ][0]["name"]

    # If we're on local VM we need to use different nameserver to
    # prevent infinite loops caused by sshuttle.
    add_custom_nameserver = args.method == "vpn-tcp" and args.in_local_vm

    if args.needs_root:
        image_name = TELEPRESENCE_REMOTE_IMAGE_PRIV
    else:
        image_name = TELEPRESENCE_REMOTE_IMAGE

    new_deployment_json, orig_container_json = new_swapped_deployment(
        deployment_json,
        container_name,
        run_id,
        image_name,
        add_custom_nameserver,
    )

    # Compute a new name that isn't too long, i.e. up to 63 characters.
    # Trim the original name until "tel-{run_id}-{pod_id}" fits.
    # https://github.com/kubernetes/community/blob/master/contributors/design-proposals/architecture/identifiers.md
    new_deployment_name = "{name:.{max_width}s}-{id}".format(
        name=deployment_json["metadata"]["name"],
        id=run_id,
        max_width=(50 - (len(run_id) + 1))
    )
    new_deployment_json["metadata"]["name"] = new_deployment_name

    def resize_original(replicas):
        """Resize the original deployment (kubectl scale)"""
        runner.check_call(
            runner.kubectl(
                "scale", "deployment", deployment_name,
                "--replicas={}".format(replicas)
            )
        )

    def delete_new_deployment(check):
        """Delete the new (copied) deployment"""
        ignore = []
        if not check:
            ignore = ["--ignore-not-found"]
        runner.check_call(
            runner.kubectl(
                "delete", "deployment", new_deployment_name, *ignore
            )
        )

    # Launch the new deployment
    runner.add_cleanup("Delete new deployment", delete_new_deployment, True)
    delete_new_deployment(False)  # Just in case
    runner.check_call(
        runner.kubectl("apply", "-f", "-"),
        input=json.dumps(new_deployment_json).encode("utf-8")
    )

    # Scale down the original deployment
    runner.add_cleanup(
        "Re-scale original deployment", resize_original,
        deployment_json["spec"]["replicas"]
    )
    resize_original(0)

    span.end()
    return new_deployment_name, run_id, orig_container_json


def new_swapped_deployment(
    old_deployment: Dict,
    container_to_update: str,
    run_id: str,
    telepresence_image: str,
    add_custom_nameserver: bool,
) -> Tuple[Dict, Dict]:
    """
    Create a new Deployment that uses telepresence-k8s image.

    Makes the following changes:

    1. Changes to single replica.
    2. Disables command, args, livenessProbe, readinessProbe, workingDir.
    3. Adds labels.
    4. Adds TELEPRESENCE_NAMESERVER env variable, if requested.
    5. Runs as root, if requested.
    6. Sets terminationMessagePolicy.
    7. Adds TELEPRESENCE_CONTAINER_NAMESPACE env variable so the forwarder does
       not have to access the k8s API from within the pod.

    Returns dictionary that can be encoded to JSON and used with kubectl apply,
    and contents of swapped out container.
    """
    new_deployment_json = deepcopy(old_deployment)
    new_deployment_json["spec"]["replicas"] = 1
    new_deployment_json["metadata"].setdefault("labels",
                                               {})["telepresence"] = run_id
    new_deployment_json["spec"]["template"]["metadata"].setdefault(
        "labels", {}
    )["telepresence"] = run_id
    for container, old_container in zip(
        new_deployment_json["spec"]["template"]["spec"]["containers"],
        old_deployment["spec"]["template"]["spec"]["containers"],
    ):
        if container["name"] == container_to_update:
            container["image"] = telepresence_image
            # Not strictly necessary for real use, but tests break without this
            # since we don't upload test images to Docker Hub:
            container["imagePullPolicy"] = "IfNotPresent"
            # Drop unneeded fields:
            for unneeded in [
                "command", "args", "livenessProbe", "readinessProbe",
                "workingDir", "lifecycle"
            ]:
                try:
                    container.pop(unneeded)
                except KeyError:
                    pass
            # We don't write out termination file:
            container["terminationMessagePolicy"] = "FallbackToLogsOnError"
            # Use custom name server if necessary:
            if add_custom_nameserver:
                container.setdefault("env", []).append({
                    "name":
                    "TELEPRESENCE_NAMESERVER",
                    "value":
                    get_alternate_nameserver()
                })
            # Add namespace environment variable to support deployments using
            # automountServiceAccountToken: false. To be used by forwarder.py
            # in the k8s-proxy.
            container.setdefault("env", []).append({
                "name":
                "TELEPRESENCE_CONTAINER_NAMESPACE",
                "valueFrom": {
                    "fieldRef": {
                        "fieldPath": "metadata.namespace"
                    }
                }
            })
            return new_deployment_json, old_container

    raise RuntimeError(
        "Couldn't find container {} in the Deployment.".
        format(container_to_update)
    )


def swap_deployment_openshift(runner: Runner, args: argparse.Namespace
                              ) -> Tuple[str, str, Dict]:
    """
    Swap out an existing DeploymentConfig.

    Returns (Deployment name, unique K8s label, JSON of original container that
    was swapped out.)

    In practice OpenShift doesn't seem to do the right thing when a
    DeploymentConfig is updated. In particular, we need to disable the image
    trigger so that we can use the new image, but the replicationcontroller
    then continues to deploy the existing image.

    So instead we use a different approach than for Kubernetes, replacing the
    current ReplicationController with one that uses the Telepresence image,
    then restores it. We delete the pods to force the RC to do its thing.
    """
    run_id = str(uuid4())
    deployment_name, *container_name = args.swap_deployment.split(":", 1)
    if container_name:
        container_name = container_name[0]
    rcs = runner.get_output(
        runner.kubectl(
            "get", "rc", "-o", "name", "--selector",
            "openshift.io/deployment-config.name=" + deployment_name
        )
    )
    rc_name = sorted(
        rcs.split(), key=lambda n: int(n.split("-")[-1])
    )[0].split("/", 1)[1]
    rc_json = json.loads(
        runner.get_output(
            runner.kubectl("get", "rc", "-o", "json", "--export", rc_name),
            stderr=STDOUT
        )
    )

    def apply_json(json_config):
        runner.check_call(
            runner.kubectl("apply", "-f", "-"),
            input=json.dumps(json_config).encode("utf-8")
        )
        # Now that we've updated the replication controller, delete pods to
        # make sure changes get applied:
        runner.check_call(
            runner.kubectl(
                "delete", "pod", "--selector", "deployment=" + rc_name
            )
        )

    runner.add_cleanup(
        "Restore original replication controller", apply_json, rc_json
    )

    # If no container name was given, just use the first one:
    if not container_name:
        container_name = rc_json["spec"]["template"]["spec"]["containers"
                                                             ][0]["name"]

    new_rc_json, orig_container_json = new_swapped_deployment(
        rc_json,
        container_name,
        run_id,
        TELEPRESENCE_REMOTE_IMAGE,
        args.method == "vpn-tcp" and args.in_local_vm,
    )
    apply_json(new_rc_json)
    return deployment_name, run_id, orig_container_json
