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

from typing import Any, Dict, Iterable

Manifest = Dict[str, Any]


def make_k8s_list(items: Iterable[Manifest]) -> Manifest:
    return {
        "apiVersion": "v1",
        "kind": "List",
        "items": list(items),
    }


def make_svc_manifest(
    name: str, labels: Dict[str, str], selector: Dict[str, str],
    ports: Dict[int, int]
) -> Manifest:
    manifest = {
        "apiVersion": "v1",
        "kind": "Service",
        "metadata": {
            "name": name,
            "labels": labels,
        },
        "spec": {
            "selector": selector,
            "type": "ClusterIP",
            "ports": [
                dict(port=k, targetPort=v, name="port-{}".format(k))
                for k, v in ports.items()
            ]
        },
    }
    return manifest


def make_pod_manifest(
    name: str, labels: Dict[str, str], pod_spec: Manifest
) -> Manifest:
    manifest = {
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {
            "name": name,
            "labels": labels,
        },
        "spec": pod_spec,
    }
    return manifest


def make_new_proxy_pod_manifest(
    name: str,
    run_id: str,
    image_name: str,
    service_account: str,
    env: Dict[str, str],
) -> Manifest:

    pod_spec = {
        "containers": [{
            "name": "telepresence",
            "image": image_name,
            "env": [dict(name=k, value=v) for k, v in env.items()],
            "resources": {
                "limits": {
                    "cpu": "1",
                    "memory": "256Mi",
                },
                "requests": {
                    "cpu": "25m",
                    "memory": "64Mi",
                },
            }
        }],
        "restartPolicy": "Never",
    }

    if service_account:
        pod_spec["serviceAccount"] = service_account

    pod = make_pod_manifest(name, dict(telepresence=run_id), pod_spec)

    return pod
