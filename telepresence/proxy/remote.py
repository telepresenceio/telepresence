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
from subprocess import CalledProcessError
from typing import Any, Dict, List, Optional

from telepresence import image_version
from telepresence.runner import Runner

from .manifest import Manifest


class RemoteInfo(object):
    """
    Information about the remote setup.

    :ivar pod_name str: The name of the pod created by the Deployment.
    :ivar container_config dict: The container within the Deployment JSON.
    :ivar container_name str: The name of the container.
    """
    def __init__(self, pod_name: str, pod_spec: Manifest) -> None:
        self.pod_name = pod_name
        self.pod_spec = pod_spec

        containers = pod_spec["containers"]  # type: List[Manifest]
        tel_containers = [
            idx for idx, c in enumerate(containers)
            if "/telepresence-" in c["image"]
        ]
        if not tel_containers:
            raise RuntimeError(
                "Could not find container with image "
                "'*/telepresence-*' in pod {}.".format(pod_name)
            )

        self._container_index = tel_containers[0]
        tel_container = containers[self._container_index]  # type: Manifest
        self.container_name = tel_container["name"]  # type: str

    def remote_telepresence_version(self) -> str:
        """Return the version used by the remote Telepresence container."""
        containers = self.pod_spec["containers"]  # type: List[Manifest]
        tel_container = containers[self._container_index]
        name, version = tel_container["image"].rsplit(":", 1)
        if name.endswith("telepresence-proxy"):
            return image_version
        return version


def get_deployment(runner: Runner, name: str) -> Dict[str, Any]:
    """
    Retrieve the Deployment/DeploymentConfig manifest named, or emit an error
    message for the user.
    """
    if ":" in name:
        name, container = name.split(":", 1)

    kube = runner.kubectl
    manifest = ""

    # Maybe try to find an OpenShift DeploymentConfig
    if kube.command == "oc" and kube.cluster_is_openshift:
        try:
            manifest = runner.get_output(
                runner.kubectl("get", "dc", name, "-o", "json"),
                reveal=True,
            )
        except CalledProcessError as exc:
            runner.show(
                "Failed to find DeploymentConfig {}:\n  {}".format(
                    name, exc.stderr
                )
            )
            runner.show("Will try regular Kubernetes Deployment.")

    # No DC or no OpenShift, look for a Deployment
    if manifest == "":
        try:
            manifest = runner.get_output(
                runner.kubectl("get", "deploy", name, "-o", "json"),
                reveal=True,
            )
        except CalledProcessError as exc:
            raise runner.fail(
                "Failed to find Deployment {}:\n  {}".format(name, exc.stderr)
            )

    # Parse the resulting manifest
    deployment = json.loads(manifest)  # This failing is likely a bug, so crash

    return deployment


def wait_for_pod(runner: Runner, remote_info: RemoteInfo) -> None:
    """Wait for the pod to start running."""
    span = runner.span()
    try:
        runner.check_call(
            runner.kubectl(
                "wait",
                "--for=condition=ready",
                "--timeout=60s",
                "pod/" + remote_info.pod_name,
            )
        )
    except CalledProcessError:
        pass
    for _ in runner.loop_until(120, 0.25):
        try:
            pod = json.loads(
                runner.get_output(
                    runner.kubectl(
                        "get", "pod", remote_info.pod_name, "-o", "json"
                    )
                )
            )
        except CalledProcessError:
            continue
        if pod["status"]["phase"] == "Running":
            for container in pod["status"]["containerStatuses"]:
                if container["name"] == remote_info.container_name and (
                    container["ready"]
                ):
                    span.end()
                    return
    span.end()
    raise RuntimeError(
        "Pod isn't starting or can't be found: {}".format(pod["status"])
    )


def get_remote_info(
    runner: Runner,
    deployment_name: str,
    unused_deployment_type: str,
    run_id: Optional[str] = None,
) -> RemoteInfo:
    """
    Given the deployment name, return a RemoteInfo object.

    If this is a Deployment we created, the run_id is also passed in - this is
    the session identifier we set for the telepresence label. Otherwise run_id
    is None and the Deployment name must be used to locate the Deployment.
    """
    span = runner.span()

    deployment = get_deployment(runner, deployment_name)
    dst_metadata = deployment["spec"]["template"]["metadata"]
    expected_labels = dst_metadata.get("labels", {})

    runner.write("Searching for Telepresence pod:")
    runner.write("  with name {}-*".format(deployment_name))
    runner.write("  with labels {}".format(expected_labels))

    cmd = "get pod -o json".split()
    if run_id:
        cmd.append("--selector=telepresence={}".format(run_id))

    for _ in runner.loop_until(120, 1):
        pods = json.loads(runner.get_output(runner.kubectl(*cmd)))["items"]
        for pod in pods:
            name = pod["metadata"]["name"]
            phase = pod["status"]["phase"]
            labels = pod["metadata"].get("labels", {})
            runner.write("Checking {}".format(name))
            if not name.startswith(deployment_name + "-"):
                runner.write("--> Name does not match")
                continue
            if phase not in ("Pending", "Running"):
                runner.write("--> Wrong phase: {}".format(phase))
                continue
            if not set(expected_labels.items()).issubset(set(labels.items())):
                runner.write("--> Labels don't match: {}".format(labels))
                continue

            runner.write("Looks like we've found our pod!\n")
            remote_info = RemoteInfo(name, pod["spec"])

            # Ensure remote container is running same version as we are:
            remote_version = remote_info.remote_telepresence_version()
            if remote_version != image_version:
                runner.write("Pod is running Tel {}".format(remote_version))
                raise runner.fail((
                    "The remote datawire/telepresence-k8s container is " +
                    "running version {}, but this tool is version {}. " +
                    "Please make sure both are running the same version."
                ).format(remote_version, image_version))

            # Wait for pod to be running:
            wait_for_pod(runner, remote_info)
            span.end()
            return remote_info

        # Didn't find pod...

    span.end()
    raise RuntimeError(
        "Telepresence pod not found for Deployment '{}'.".
        format(deployment_name)
    )


def make_remote_info_from_pod(pod: Manifest) -> RemoteInfo:
    pod_name = pod["metadata"]["name"]  # type: str
    pod_spec = pod["spec"]  # type: Manifest
    return RemoteInfo(pod_name, pod_spec)
