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

from telepresence.outbound.local import launch_inject, launch_vpn
from telepresence.runner import Runner


def setup_inject(runner: Runner, args):
    runner.require(["torsocks"], "Please install torsocks (v2.1 or later)")
    if runner.chatty:
        runner.show(
            "Starting proxy with method 'inject-tcp', which has the following "
            "limitations:"
        )
        runner.show(
            "Go programs, static binaries, suid programs, and custom DNS "
            "implementations are not supported."
        )
        runner.show(
            "For a full list of method limitations see "
            "https://telepresence.io/reference/methods.html"
        )
    command = ["torsocks"] + (args.run or ["bash" "--norc"])

    def launch(runner_, _remote_info, env, socks_port, _ssh):
        return launch_inject(runner_, command, socks_port, env)

    return launch


def setup_vpn(runner: Runner, args):
    runner.require(["sshuttle-telepresence"],
                   "Part of the Telepresence package. Try reinstalling.")
    if runner.platform == "linux":
        # Need conntrack for sshuttle on Linux:
        runner.require(["conntrack", "iptables"],
                       "Required for the vpn-tcp method")
    if runner.platform == "darwin":
        runner.require(["pfctl"], "Required for the vpn-tcp method")
    runner.require_sudo()
    if runner.chatty:
        runner.show(
            "Starting proxy with method 'vpn-tcp', which has the following "
            "limitations:"
        )
        runner.show(
            "All processes are affected, only one telepresence can run per "
            "machine, and you can't use other VPNs. You may need to add cloud "
            "hosts and headless services with --also-proxy."
        )
        runner.show(
            "For a full list of method limitations see "
            "https://telepresence.io/reference/methods.html"
        )
    command = args.run or ["bash" "--norc"]

    def launch(runner_, remote_info, env, _socks_port, ssh):
        return launch_vpn(
            runner_, remote_info, command, args.also_proxy, env, ssh
        )

    return launch


def setup_container(runner: Runner, args):
    runner.require(["docker", "socat"], "Needed for the container method.")
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


def setup(runner: Runner, args):
    if args.method == "inject-tcp":
        return setup_inject(runner, args)

    if args.method == "vpn-tcp":
        return setup_vpn(runner, args)

    if args.method == "container":
        return setup_container(runner, args)

    assert False, args.method
