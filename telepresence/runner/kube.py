# Copyright 2019 Datawire. All rights reserved.
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

import typing

_KubeInfo = typing.NamedTuple(
    "_KubeInfo", [
        ("cluster", str),
        ("cluster_version", str),
        ("cluster_is_openshift", bool),
        ("command", str),
        ("command_version", str),
        ("server", str),
        ("context", str),
        ("namespace", str),
        ("in_local_vm", bool),
        ("verbose", bool),
    ]
)


class KubeInfo(_KubeInfo):
    def __call__(self, *in_args: str) -> typing.List[str]:
        assert self.cluster, self
        result = [self.command]
        if self.verbose:
            # result.append("--v=4")  # See issue #807
            pass
        result.extend(["--context", self.context])
        result.extend(["--namespace", self.namespace])
        result += in_args
        return result


KUBE_UNSET = KubeInfo("", "", False, "", "", "", "", "", False, False)
