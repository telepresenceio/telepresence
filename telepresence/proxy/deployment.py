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
from copy import deepcopy
from subprocess import CalledProcessError
from typing import Dict, Optional, Tuple

from telepresence import (
    TELEPRESENCE_REMOTE_IMAGE, TELEPRESENCE_REMOTE_IMAGE_OCP,
    TELEPRESENCE_REMOTE_IMAGE_PRIV
)
from telepresence.cli import PortMapping
from telepresence.runner import Runner

from .remote import get_deployment


def get_image_name(runner: Runner, expose: PortMapping) -> str:
    """
    Return the correct Telepresence image name (OpenShift-specific, privileged,
    or not) accounting for the existence of an OpenShift cluster, user
    overrides, and the use of privileged ports (< 1024).
    """
    ocp_env_name = "TELEPRESENCE_USE_OCP_IMAGE"
    ocp_env_value = os.environ.get(ocp_env_name, "auto")
    ocp_env = ocp_env_value.lower()
    if ocp_env in ("true", "on", "yes", "1", "always"):
        return TELEPRESENCE_REMOTE_IMAGE_OCP

    ocp_image_allowed = True
    if ocp_env in ("false", "off", "no", "0", "never"):
        ocp_image_allowed = False
    elif ocp_env not in ("auto", "automatic", "default"):
        runner.show(
            "\nWARNING: Ignoring {} environment variable with value {!r}. "
            "Accepted values are YES or NO or AUTO. "
            "Using AUTO.".format(ocp_env_name, ocp_env_value)
        )

    if ocp_image_allowed and runner.kubectl.cluster_is_openshift:
        return TELEPRESENCE_REMOTE_IMAGE_OCP
    if expose.has_privileged_ports():
        return TELEPRESENCE_REMOTE_IMAGE_PRIV
    return TELEPRESENCE_REMOTE_IMAGE


def existing_deployment(
    runner: Runner,
    deployment_arg: str,
    expose: PortMapping,
    deployment_env: Dict,
    service_account: str,
) -> Tuple[str, Optional[str]]:
    """
    Handle an existing deployment by doing nothing
    """
    runner.show(
        "Starting network proxy to cluster using the existing proxy "
        "Deployment {}".format(deployment_arg)
    )
    try:
        d_json = json.loads(
            runner.get_output(
                runner.kubectl(
                    "get", "deployment", deployment_arg, "-o", "json"
                )
            )
        )

        _set_expose_ports(expose, deployment_arg, d_json)
    except CalledProcessError as exc:
        raise runner.fail(
            "Failed to find deployment {}:\n{}".format(
                deployment_arg, exc.stderr
            )
        )
    run_id = None
    return deployment_arg, run_id


def existing_deployment_openshift(
    runner: Runner,
    deployment_arg: str,
    expose: PortMapping,
    deployment_env: Dict,
    service_account: str,
) -> Tuple[str, Optional[str]]:
    """
    Handle an existing deploymentconfig by doing nothing
    """
    runner.show(
        "Starting network proxy to cluster using the existing proxy "
        "DeploymentConfig {}".format(deployment_arg)
    )
    try:
        d_json = json.loads(
            runner.get_output(
                runner.kubectl(
                    "get", "deploymentconfig", deployment_arg, "-o", "json"
                )
            )
        )

        _set_expose_ports(expose, deployment_arg, d_json)
    except CalledProcessError as exc:
        raise runner.fail(
            "Failed to find deploymentconfig {}:\n{}".format(
                deployment_arg, exc.stderr
            )
        )
    run_id = None
    return deployment_arg, run_id


def _set_expose_ports(expose, deployment_arg, d_json):
    deployment, container = _split_deployment_container(deployment_arg)
    container_to_update = _get_container_name(container, d_json)

    for container in d_json["spec"]["template"]["spec"]["containers"]:
        if container["name"] == container_to_update:
            # Merge container ports into the expose list
            expose.merge_automatic_ports([
                port["containerPort"] for port in container.get("ports", [])
                if port["protocol"] == "TCP"
            ])
            break


_deployment_template = """apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    telepresence: {run_id}
  name: {name}
spec:
  selector:
    matchLabels:
      telepresence: {run_id}
  template:
    metadata:
      labels:
        telepresence: {run_id}
    spec:
      containers:
      - {env_field}image: {image_name}
        name: {name}
        resources:
          limits:
            cpu: "1"
            memory: 256Mi
          requests:
            cpu: 25m
            memory: 64Mi
      {service_account_field}
"""


def _get_deployment_yaml(
    name: str,
    run_id: str,
    image_name: str,
    service_account: str,
    env: Dict,
) -> str:
    service_account_field = ""
    if service_account:
        service_account_field = "serviceAccount: %s" % service_account
    env_field = ""
    if env:
        env_lines = ["env:\n"]
        for key, value in env.items():
            env_lines.append("        - name: %s\n" % key)
            env_lines.append("          value: %s\n" % value)
        env_lines.append("        ")
        env_field = "".join(env_lines)
    return _deployment_template.format(
        name=name,
        run_id=run_id,
        image_name=image_name,
        env_field=env_field,
        service_account_field=service_account_field,
    )


def create_new_deployment(
    runner: Runner,
    deployment_arg: str,
    expose: PortMapping,
    deployment_env: Dict,
    service_account: str,
) -> Tuple[str, str]:
    """
    Create a new Deployment, return its name and Kubernetes label.
    """
    span = runner.span()
    run_id = runner.session_id
    runner.show(
        "Starting network proxy to cluster using "
        "new Deployment {}".format(deployment_arg)
    )

    def remove_existing_deployment(quiet=False):
        if not quiet:
            runner.show("Cleaning up Deployment {}".format(deployment_arg))
        runner.check_call(
            runner.kubectl(
                "delete",
                "--ignore-not-found",
                "svc,deploy",
                "--selector=telepresence=" + run_id,
            )
        )

    runner.add_cleanup("Delete new deployment", remove_existing_deployment)
    remove_existing_deployment(quiet=True)
    # Define the deployment as yaml
    # Create the deployment via yaml
    deployment_yaml = _get_deployment_yaml(
        deployment_arg,
        run_id,
        get_image_name(runner, expose),
        service_account,
        deployment_env,
    )
    try:
        runner.check_call(
            runner.kubectl("create", "-f", "-"),
            input=deployment_yaml.encode("utf-8")
        )
    except CalledProcessError as exc:
        raise runner.fail(
            "Failed to create deployment {}:\n{}".format(
                deployment_arg, exc.stderr
            )
        )
    # Expose the deployment with a service
    if expose.remote():
        command = [
            "expose",
            "deployment",
            deployment_arg,
        ]
        # Provide a stable argument ordering.  Reverse it because that
        # happens to make some current tests happy but in the long run
        # that's totally arbitrary and doesn't need to be maintained.
        # See issue 494.
        for port in sorted(expose.remote(), reverse=True):
            command.append("--port={}".format(port))
        try:
            runner.check_call(runner.kubectl(*command))
        except CalledProcessError as exc:
            raise runner.fail(
                "Failed to expose deployment {}:\n{}".format(
                    deployment_arg, exc.stderr
                )
            )
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


def supplant_deployment(
    runner: Runner,
    deployment_arg: str,
    expose: PortMapping,
    deployment_env: Dict,
    service_account: str,
) -> Tuple[str, str]:
    """
    Swap out an existing Deployment, supplant method.

    Native Kubernetes version.

    Returns (Deployment name, unique K8s label, JSON of original container that
    was swapped out.)
    """
    span = runner.span()
    run_id = runner.session_id

    runner.show(
        "Starting network proxy to cluster by swapping out "
        "Deployment {} with a proxy".format(deployment_arg)
    )

    deployment, container = _split_deployment_container(deployment_arg)
    deployment_json = get_deployment(runner, deployment)
    container = _get_container_name(container, deployment_json)

    new_deployment_json = new_swapped_deployment(
        runner,
        deployment_json,
        container,
        run_id,
        expose,
        service_account,
        deployment_env,
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
        else:
            runner.show(
                "Swapping Deployment {} back to its original state".
                format(deployment_arg)
            )
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
    return new_deployment_name, run_id


def new_swapped_deployment(
    runner: Runner,
    old_deployment: Dict,
    container_to_update: str,
    run_id: str,
    expose: PortMapping,
    service_account: str,
    deployment_env: Dict,
) -> Dict:
    """
    Create a new Deployment that uses telepresence-k8s image.

    Makes the following changes:

    1. Changes to single replica.
    2. Disables command, args, livenessProbe, readinessProbe, workingDir.
    3. Adds labels.
    4. Adds environment variables.
    5. Runs as root, if requested.
    6. Sets terminationMessagePolicy.
    7. Adds TELEPRESENCE_CONTAINER_NAMESPACE env variable so the forwarder does
       not have to access the k8s API from within the pod.

    Returns dictionary that can be encoded to JSON and used with kubectl apply.
    Mutates the passed-in PortMapping to include container ports.
    """
    new_deployment_json = deepcopy(old_deployment)
    new_deployment_json["spec"]["replicas"] = 1
    new_deployment_json["metadata"].setdefault("labels",
                                               {})["telepresence"] = run_id
    ndj_template = new_deployment_json["spec"]["template"]
    ndj_template["metadata"].setdefault("labels", {})["telepresence"] = run_id
    if service_account:
        ndj_template["spec"]["serviceAccountName"] = service_account
    for container, old_container in zip(
        ndj_template["spec"]["containers"],
        old_deployment["spec"]["template"]["spec"]["containers"],
    ):
        if container["name"] == container_to_update:
            # Merge container ports into the expose list
            expose.merge_automatic_ports([
                port["containerPort"] for port in container.get("ports", [])
                if port["protocol"] == "TCP"
            ])
            container["image"] = get_image_name(runner, expose)
            # Not strictly necessary for real use, but tests break without this
            # since we don't upload test images to Docker Hub:
            container["imagePullPolicy"] = "IfNotPresent"
            # Drop unneeded fields:
            for unneeded in [
                "args", "startupProbe", "livenessProbe", "readinessProbe",
                "workingDir", "lifecycle"
            ]:
                try:
                    container.pop(unneeded)
                except KeyError:
                    pass
            # Set running command explicitly
            container["command"] = ["/usr/src/app/run.sh"]
            # We don't write out termination file:
            container["terminationMessagePolicy"] = "FallbackToLogsOnError"
            # Use custom name server if necessary:
            if deployment_env:
                for key, value in deployment_env.items():
                    container.setdefault("env", []).append({
                        "name": key,
                        "value": value,
                    })
            # Add namespace environment variable to support deployments using
            # automountServiceAccountToken: false. To be used by forwarder.py
            # in the k8s-proxy.
            container.setdefault("env", []).append({
                "name": "TELEPRESENCE_CONTAINER_NAMESPACE",
                "valueFrom": {
                    "fieldRef": {
                        "fieldPath": "metadata.namespace"
                    }
                }
            })
            return new_deployment_json

    raise RuntimeError(
        "Couldn't find container {} in the Deployment.".
        format(container_to_update)
    )


def swap_deployment_openshift(
    runner: Runner,
    deployment_arg: str,
    expose: PortMapping,
    deployment_env: Dict,
    service_account: str,
) -> Tuple[str, str]:
    """
    Swap out an existing DeploymentConfig and also clears any triggers
    which were registered, otherwise replaced telepresence pod would
    be immediately swapped back to the original one because of
    image change trigger.

    Returns (Deployment name, unique K8s label, JSON of original container that
    was swapped out.)

    """

    run_id = runner.session_id
    deployment, container = _split_deployment_container(deployment_arg)

    dc_json_with_triggers = json.loads(
        runner.get_output(
            runner.kubectl(
                "get", "dc/{}".format(deployment), "-o", "json", "--export"
            )
        )
    )

    runner.check_call(
        runner.kubectl(
            "set", "triggers", "dc/{}".format(deployment), "--remove-all"
        )
    )

    dc_json = json.loads(
        runner.get_output(
            runner.kubectl(
                "get", "dc/{}".format(deployment), "-o", "json", "--export"
            )
        )
    )

    def apply_json(json_config):
        runner.check_call(
            runner.kubectl("replace", "-f", "-"),
            input=json.dumps(json_config).encode("utf-8")
        )
        # Now that we've updated the deployment config,
        # let's rollout latest version to apply the changes
        runner.check_call(
            runner.kubectl("rollout", "latest", "dc/{}".format(deployment))
        )

        runner.check_call(
            runner.kubectl(
                "rollout", "status", "-w", "dc/{}".format(deployment)
            )
        )

    runner.add_cleanup(
        "Restore original deployment config", apply_json, dc_json_with_triggers
    )

    container = _get_container_name(container, dc_json)

    new_dc_json = new_swapped_deployment(
        runner,
        dc_json,
        container,
        run_id,
        expose,
        service_account,
        deployment_env,
    )

    apply_json(new_dc_json)

    return deployment, run_id
