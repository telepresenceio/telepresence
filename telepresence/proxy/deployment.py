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
from copy import deepcopy
from subprocess import STDOUT
from typing import Tuple, Dict, Optional

from telepresence.cli import PortMapping
from telepresence.proxy.remote import get_deployment_json
from telepresence.runner import Runner
from telepresence.utilities import get_alternate_nameserver


def existing_deployment(
    runner: Runner, deployment_arg: str, image_name: str, expose: PortMapping,
    add_custom_nameserver: bool, forward_traffic: bool
) -> Tuple[str, Optional[str]]:
    """
    Handle an existing deployment by doing nothing
    """
    run_id = None
    return deployment_arg, run_id


def create_new_deployment(
    runner: Runner, deployment_arg: str, image_name: str, expose: PortMapping,
    add_custom_nameserver: bool, forward_traffic: bool
) -> Tuple[str, str]:
    """
    Create a new Deployment, return its name and Kubernetes label.
    """
    span = runner.span()
    run_id = runner.session_id

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
    command = [
        "run",  # This will result in using Deployment:
        "--restart=Always",
        "--limits=cpu=100m,memory=256Mi",
        "--requests=cpu=25m,memory=64Mi",
        deployment_arg,
        "--image=" + image_name,
        "--labels=telepresence=" + run_id,
    ]
    # Provide a stable argument ordering.  Reverse it because that happens to
    # make some current tests happy but in the long run that's totally
    # arbitrary and doesn't need to be maintained.  See issue 494.
    for port in sorted(expose.remote(), reverse=True):
        command.append("--port={}".format(port))
    if expose.remote():
        command.append("--expose")
    # If we're on local VM we need to use different nameserver to prevent
    # infinite loops caused by sshuttle:
    if add_custom_nameserver:
        command.append(
            "--env=TELEPRESENCE_NAMESERVER=" + get_alternate_nameserver()
        )
    runner.get_output(runner.kubectl(command))
    span.end()
    return deployment_arg, run_id


def _split_deployment_container(deployment_arg):
    deployment, *container = deployment_arg.split(":", 1)
    if container:
        container = container[0]
    return deployment, container


def _get_container_name(container, deployment_json):
    # If no container name was given, just use the first one:
    if not container:
        spec = deployment_json["spec"]["template"]["spec"]
        container = spec["containers"][0]["name"]
    return container


def _merge_expose_ports(expose, container_json):
    expose.merge_automatic_ports([
        port["containerPort"] for port in container_json.get("ports", [])
        if port["protocol"] == "TCP"
    ])


def swap_deployment(
    runner: Runner, deployment_arg: str, image_name: str, expose: PortMapping,
    add_custom_nameserver: bool, forward_traffic: bool
) -> Tuple[str, str]:
    return supplant_deployment(
        runner, deployment_arg, image_name, expose,
        add_custom_nameserver, True, True)


def copy_deployment(
    runner: Runner, deployment_arg: str, image_name: str, expose: PortMapping,
    add_custom_nameserver: bool, forward_traffic: bool
) -> Tuple[str, str]:
    return supplant_deployment(
        runner, deployment_arg, image_name, expose,
        add_custom_nameserver, forward_traffic, False)


def supplant_deployment(
    runner: Runner, deployment_arg: str, image_name: str, expose: PortMapping,
    add_custom_nameserver: bool, forward_traffic: bool, zero_original: bool
) -> Tuple[str, str]:
    """
    Swap out an existing Deployment, supplant method.

    Native Kubernetes version.

    Returns (Deployment name, unique K8s label, JSON of original container that
    was swapped out.)
    """
    span = runner.span()
    run_id = runner.session_id

    deployment, container = _split_deployment_container(deployment_arg)
    deployment_json = get_deployment_json(
        runner,
        deployment,
        "deployment",
    )
    container = _get_container_name(container, deployment_json)

    new_deployment_json, orig_container_json = new_swapped_deployment(
        deployment_json,
        container,
        run_id,
        image_name,
        add_custom_nameserver,
        forward_traffic
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
                "scale", "deployment", deployment,
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

    if zero_original:
        # Scale down the original deployment
        runner.add_cleanup(
            "Re-scale original deployment", resize_original,
            deployment_json["spec"]["replicas"]
        )
        resize_original(0)

    if forward_traffic:
        _merge_expose_ports(expose, orig_container_json)

    span.end()
    return new_deployment_name, run_id


def new_swapped_deployment(
    old_deployment: Dict,
    container_to_update: str,
    run_id: str,
    telepresence_image: str,
    add_custom_nameserver: bool,
    forward_traffic: bool,
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
    8. Forward traffic, if requested

    Returns dictionary that can be encoded to JSON and used with kubectl apply,
    and contents of swapped out container.
    """
    new_deployment_json = deepcopy(old_deployment)
    new_deployment_json["spec"]["replicas"] = 1

    if not forward_traffic:
        new_deployment_json["metadata"]["labels"] = {}
        new_deployment_json["spec"]["template"]["metadata"]["labels"] = {}
        new_deployment_json["spec"]["selector"] = None

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


def swap_deployment_openshift(
    runner: Runner, deployment_arg: str, image_name: str, expose: PortMapping,
    add_custom_nameserver: bool, forward_traffic: bool
) -> Tuple[str, str]:
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
    run_id = runner.session_id
    deployment, container = _split_deployment_container(deployment_arg)
    rcs = runner.get_output(
        runner.kubectl(
            "get", "rc", "-o", "name", "--selector",
            "openshift.io/deployment-config.name=" + deployment
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

    container = _get_container_name(container, rc_json)

    new_rc_json, orig_container_json = new_swapped_deployment(
        rc_json,
        container,
        run_id,
        image_name,
        add_custom_nameserver,
        forward_traffic
    )
    apply_json(new_rc_json)

    _merge_expose_ports(expose, orig_container_json)

    return deployment, run_id
