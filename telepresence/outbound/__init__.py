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


def setup(runner, args):
    if runner.chatty and args.method != "container":
        runner.show(
            "Starting proxy with method '{}', which has the following "
            "limitations:".format(args.method)
        )
        if args.method == "vpn-tcp":
            runner.show(
                "All processes are affected, only one telepresence"
                " can run per machine, and you can't use other VPNs."
                " You may need to add cloud hosts with --also-proxy."
            )
        elif args.method == "inject-tcp":
            runner.show(
                "Go programs, static binaries, suid programs, and custom DNS"
                " implementations are not supported."
            )
        runner.show(
            "For a full list of method limitations see "
            "https://telepresence.io/reference/methods.html\n"
        )
