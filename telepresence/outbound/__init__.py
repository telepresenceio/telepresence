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
    if args.method == "inject-tcp":
        runner.require(["torsocks"], "Please install torsocks (v2.1 or later)")

    if args.method == "vpn-tcp":
        runner.require(["sshuttle-telepresence"],
                       "Part of the Telepresence package. Try reinstalling.")
        if runner.platform == "linux":
            # Need conntrack for sshuttle on Linux:
            runner.require(["conntrack", "iptables"],
                           "Required for the vpn-tcp method")
        if runner.platform == "darwin":
            runner.require(["pfctl"], "Required for the vpn-tcp method")

    if args.method == "container":
        runner.require(
            ["docker", "socat"],
            "Needed for the container method.",
        )
        if runner.platform == "linux":
            needed = ["ip", "ifconfig"]
            missing = runner.depend(needed)
            if set(needed) == set(missing):
                raise runner.fail(
                    """At least one of "ip addr" or "ifconfig" must be """ +
                    "available to retrieve Docker interface info."
                )

        if runner.platform == "darwin":
            runner.require(
                ["ifconfig"],
                "Needed to manage networking with the container method.",
            )
            runner.require_sudo()

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
