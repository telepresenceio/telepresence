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
from subprocess import STDOUT, CalledProcessError
from time import time, sleep
from typing import Optional, Dict

from telepresence import image_version
from telepresence.runner import Runner


class RemoteInfo(object):
    """
    Information about the remote setup.

    :ivar namespace str: The Kubernetes namespace.
    :ivar context str: The Kubernetes context.
    :ivar deployment_name str: The name of the Deployment object.
    :ivar pod_name str: The name of the pod created by the Deployment.
    :ivar deployment_config dict: The decoded k8s object (i.e. JSON/YAML).
    :ivar container_config dict: The container within the Deployment JSON.
    :ivar container_name str: The name of the container.
    """

    def __init__(
        self,
        runner: Runner,
        context: str,
        namespace: str,
        deployment_name: str,
        pod_name: str,
        deployment_config: dict,
    ) -> None:
        self.context = context
        self.namespace = namespace
        self.deployment_name = deployment_name
        self.pod_name = pod_name
        self.deployment_config = deployment_config
        cs = deployment_config["spec"]["template"]["spec"]["containers"]
        containers = [c for c in cs if "telepresence-k8s" in c["image"]]
        if not containers:
            raise RuntimeError(
                "Could not find container with image "
                "'datawire/telepresence-k8s' in pod {}.".format(pod_name)
            )
        self.container_config = containers[0]  # type: Dict
        self.container_name = self.container_config["name"]  # type: str

    def remote_telepresence_version(self) -> str:
        """Return the version used by the remote Telepresence container."""
        return self.container_config["image"].split(":")[-1]


def get_deployment_json(
    runner: Runner,
    deployment_name: str,
    context: str,
    namespace: str,
    deployment_type: str,
    run_id: Optional[str] = None,
) -> Dict:
    """Get the decoded JSON for a deployment.

    If this is a Deployment we created, the run_id is also passed in - this is
    the session id we set for the telepresence label. Otherwise run_id is None
    and the Deployment name must be used to locate the Deployment.
    """
    assert context is not None
    assert namespace is not None
    span = runner.span()
    try:
        get_deployment = [
            "get",
            deployment_type,
            "-o",
            "json",
            "--export",
        ]
        if run_id is None:
            return json.loads(
                runner.get_output(
                    runner.kubectl(get_deployment + [deployment_name]),
                    stderr=STDOUT
                )
            )
        else:
            # When using a selector we get a list of objects, not just one:
            return json.loads(
                runner.get_output(
                    runner.kubectl(
                        get_deployment + ["--selector=telepresence=" + run_id]
                    ),
                    stderr=STDOUT
                )
            )["items"][0]
    except CalledProcessError as e:
        raise runner.fail(
            "Failed to find Deployment '{}':\n{}".format(
                deployment_name, e.stdout
            )
        )
    finally:
        span.end()


def wait_for_pod(runner: Runner, remote_info: RemoteInfo) -> None:
    """Wait for the pod to start running."""
    span = runner.span()
    start = time()
    while time() - start < 120:
        try:
            pod = json.loads(
                runner.get_output(
                    runner.kubectl(
                        "get", "pod", remote_info.pod_name, "-o", "json"
                    )
                )
            )
        except CalledProcessError:
            sleep(0.25)
            continue
        if pod["status"]["phase"] == "Running":
            for container in pod["status"]["containerStatuses"]:
                if container["name"] == remote_info.container_name and (
                    container["ready"]
                ):
                    span.end()
                    return
        sleep(0.25)
    span.end()
    raise RuntimeError(
        "Pod isn't starting or can't be found: {}".format(pod["status"])
    )


def get_remote_info(
    runner: Runner,
    deployment_name: str,
    context: str,
    namespace: str,
    deployment_type: str,
    run_id: Optional[str] = None,
) -> RemoteInfo:
    """
    Given the deployment name, return a RemoteInfo object.

    If this is a Deployment we created, the run_id is also passed in - this is
    the session identifier we set for the telepresence label. Otherwise run_id
    is None and the Deployment name must be used to locate the Deployment.
    """
    span = runner.span()
    deployment = get_deployment_json(
        runner,
        deployment_name,
        context,
        namespace,
        deployment_type,
        run_id=run_id
    )
    dst_metadata = deployment["spec"]["template"]["metadata"]
    expected_labels = dst_metadata.get("labels", {})

    # Metadata for Deployment will hopefully have a namespace. If not,
    # fall back to one we were given. If we weren't given one, best we
    # can do is choose "default".
    deployment_namespace = deployment["metadata"].get("namespace", namespace)

    runner.write("Searching for Telepresence pod:")
    runner.write("  with name {}-*".format(deployment_name))
    runner.write("  with labels {}".format(expected_labels))
    runner.write("  with namespace {}".format(deployment_namespace))

    cmd = "get pod -o json --export".split()
    if run_id:
        cmd.append("--selector=telepresence={}".format(run_id))

    start = time()
    while time() - start < 120:
        pods = json.loads(runner.get_output(runner.kubectl(cmd)))["items"]
        for pod in pods:
            name = pod["metadata"]["name"]
            phase = pod["status"]["phase"]
            labels = pod["metadata"].get("labels", {})
            pod_ns = pod["metadata"]["namespace"]
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
            if pod_ns != deployment_namespace:
                runner.write("--> Wrong namespace: {}".format(pod_ns))
                continue

            runner.write("Looks like we've found our pod!\n")
            remote_info = RemoteInfo(
                runner,
                context,
                namespace,
                deployment_name,
                name,
                deployment,
            )

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
        sleep(1)

    span.end()
    raise RuntimeError(
        "Telepresence pod not found for Deployment '{}'.".
        format(deployment_name)
    )
