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

import json
from subprocess import CalledProcessError
from typing import Callable, Dict, Iterable, List, NamedTuple, Optional, Tuple

from telepresence import image_version
from telepresence.cli import PortMapping
from telepresence.runner import Runner

from .deployment import get_image_name
from .manifest import (
    Manifest, make_k8s_list, make_new_proxy_pod_manifest, make_svc_manifest
)
from .remote import (
    RemoteInfo, get_remote_info, make_remote_info_from_pod, wait_for_pod,
    get_deployment, get_pod_for_deployment
)

ProxyIntent = NamedTuple(
    "ProxyIntent", [
        ("name", str),
        ("container", str),
        ("expose", PortMapping),
        ("env", Dict[str, str]),
        ("service_account", str),
    ]
)


class ProxyOperation:
    def __init__(self, intent: ProxyIntent) -> None:
        self.intent = intent
        self.remote_info = None  # type: Optional[RemoteInfo]

    def prepare(self, runner: Runner) -> None:
        pass

    def act(self, _: Runner) -> RemoteInfo:
        raise NotImplementedError()


LegacyOperation = Callable[[Runner, str, PortMapping, Dict[str, str], str],
                           Tuple[str, Optional[str]]]


class Legacy(ProxyOperation):
    def __init__(self, intent: ProxyIntent, legacy_op: LegacyOperation):
        super().__init__(intent)
        self.op = legacy_op

    def act(self, runner: Runner) -> RemoteInfo:
        deployment_arg = self.intent.name
        if self.intent.container:
            deployment_arg += ":" + self.intent.container

        tel_deployment, run_id = self.op(
            runner,
            deployment_arg,
            self.intent.expose,
            self.intent.env,
            self.intent.service_account,
        )

        remote_info = get_remote_info(
            runner,
            tel_deployment,
            "unused, right?",
            run_id=run_id,
        )

        return remote_info


class New(ProxyOperation):
    def prepare(self, runner: Runner) -> None:
        self.manifests = []  # type: List[Manifest]

        # Construct a Pod manifest
        pod = make_new_proxy_pod_manifest(
            self.intent.name,
            runner.session_id,
            get_image_name(runner, self.intent.expose),
            self.intent.service_account,
            self.intent.env,
        )
        self.manifests.append(pod)

        # Construct a Service manifest as needed
        if self.intent.expose.remote():
            svc = make_svc_manifest(
                self.intent.name,
                dict(telepresence=runner.session_id),
                dict(telepresence=runner.session_id),
                {p: p
                 for p in self.intent.expose.remote()},
            )
            self.manifests.append(svc)

        self.remote_info = make_remote_info_from_pod(pod)

    def act(self, runner: Runner) -> RemoteInfo:
        assert self.remote_info is not None

        runner.show(
            "Starting network proxy to cluster using "
            "new Pod {}".format(self.intent.name)
        )

        manifest_list = make_k8s_list(self.manifests)
        manifest_json = json.dumps(manifest_list)
        try:
            runner.check_call(
                runner.kubectl("create", "-f", "-"),
                input=manifest_json.encode("utf-8")
            )
        except CalledProcessError as exc:
            raise runner.fail(
                "Failed to create Pod/Service {}:\n{}".format(
                    self.intent.name, exc.stderr
                )
            )

        def clean_up():
            runner.show("Cleaning up Pod/Service {}".format(self.intent.name))
            runner.check_call(
                runner.kubectl(
                    "delete",
                    "--ignore-not-found",
                    "--wait=false",
                    "--selector=telepresence=" + runner.session_id,
                    "svc,pod",
                )
            )

        runner.add_cleanup("Delete new Pod/Service", clean_up)

        wait_for_pod(runner, self.remote_info)

        return self.remote_info


"""
class Swap(ProxyOperation):
    def prepare(self, runner: Runner) -> None:
        # Grab original deployment's Pod Config
        deployment = get_deployment(runner, name)  # from .remote

        # Compute proxy Pod's manifest
        pod_spec = deployment["spec"]["template"]["spec"]
        # TODO: perform the usual swap changes
        # TODO: rip off from new_swapped_deployment(...)
        # FIXME: Implement this...

        # FIXME: Copy-pasta from New.prepare(...)
        # FIXME: factor out more of making a Tel pod?
        pod = make_new_proxy_pod_manifest(...)

        self.remote_info = make_remote_info_from_pod(pod)

    def act(self, runner: Runner) -> RemoteInfo:
        assert self.remote_info is not None

        # FIXME: Copy-pasta from New.act(...)
        # Apply the manifest
        # Set up for cleanup

        # FIXME: Factor this out?
        # This all seems repetitive

        wait_for_pod(runner, self.remote_info)

        return self.remote_info

"""


def ensure_correct_version(runner: Runner, remote_info: RemoteInfo) -> None:
    """
    Ensure remote container is running same version as we are
    """
    remote_version = remote_info.remote_telepresence_version()
    if remote_version != image_version:
        runner.write("Pod is running Tel {}".format(remote_version))
        raise runner.fail((
            "The remote datawire/telepresence-k8s container is " +
            "running version {}, but this tool is version {}. " +
            "Please make sure both are running the same version."
        ).format(remote_version, image_version))


def set_expose_ports(
    expose: PortMapping, pod: Manifest, container_name: str
) -> None:
    """
    Merge container ports into the expose list
    """
    containers = pod["spec"]["containers"]  # type: Iterable[Manifest]
    for container in containers:
        if not container_name or container["name"] == container_name:
            expose.merge_automatic_ports([
                port["containerPort"] for port in container.get("ports", [])
                if port["protocol"] == "TCP"
            ])
            break


class Existing(ProxyOperation):
    def prepare(self, runner: Runner) -> None:
        deployment = get_deployment(runner, self.intent.name)  # type: Manifest
        pod = get_pod_for_deployment(runner, deployment)  # type: Manifest
        set_expose_ports(self.intent.expose, pod, self.intent.container)
        self.remote_info = RemoteInfo(pod["metadata"]["name"], pod["spec"])
        ensure_correct_version(runner, self.remote_info)

    def act(self, runner: Runner) -> RemoteInfo:
        assert self.remote_info is not None

        runner.show(
            "Starting network proxy to cluster using "
            "the existing proxy Deployment {}".format(self.intent.name)
        )

        wait_for_pod(runner, self.remote_info)

        return self.remote_info
