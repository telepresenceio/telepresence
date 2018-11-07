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
from telepresence.outbound.container import setup_container
from telepresence.outbound.local import setup_inject, setup_vpn
from telepresence.runner import Runner


def setup(runner: Runner, args):
    if args.method == "inject-tcp":
        return setup_inject(runner, args)

    if args.method == "vpn-tcp":
        return setup_vpn(runner, args)

    if args.method == "container":
        return setup_container(runner, args)

    assert False, args.method
