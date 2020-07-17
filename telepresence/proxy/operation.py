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

from typing import Callable, Dict, NamedTuple, Optional, Tuple

from telepresence.cli import PortMapping
from telepresence.runner import Runner

from .remote import RemoteInfo, get_remote_info

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
