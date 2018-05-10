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
"""
This commands has two modes: proxy, and wait.

== Proxy mode ==
Run sshuttle-telepresence via SSH IP and port given on command line.

The SSH server will run on the host, so the sshuttle-telepresence talking to it
somehow needs to get the IP of the host. So we read port and an optional IP
from the command line.

* If host is macOS an IP will be given.
* If host is Linux no IP will be given, and then we fall back to IP of default
  route.

The program expects to receive a JSON-encoded object as command line argument,
with parameters:

1. "port", the port number to connect ssh to.
2. "ip", optional, the ip of the ssh server.
3. "cidrs", a list of CIDRs for sshuttle.

References:

* https://stackoverflow.com/q/22944631/7862510
* https://docs.docker.com/docker-for-mac/networking/


== Wait mode ==

Wait mode should be run in same network namespace as the proxy. It will do the
'hellotelepresence' loop used to correct DNS on the k8s proxy, and to detect
when the proxy is working.

When the process exits with exit code 100 that means the proxy is active.
"""

import sys
from json import loads
from subprocess import check_output, Popen
from socket import gethostbyname, gaierror
from time import time, sleep

from telepresence.expose import expose_local_services
from telepresence.ssh import SSH
from telepresence.cleanup import Subprocesses, wait_for_exit
from telepresence.runner import Runner


def main():
    """Dispatch to the correct mode"""
    command = sys.argv[1]
    if command == "proxy":
        proxy(loads(sys.argv[2]))
    elif command == "wait":
        wait()


def proxy(config: dict):
    """Start sshuttle proxy to Kubernetes."""
    port = config["port"]
    if "ip" in config:
        # Typically host is macOS:
        ip = config["ip"]
    else:
        # Typically host is Linux, use default route:
        ip = None
        route_output = str(check_output(["route", "-n"]), "ascii")
        for line in route_output.splitlines():
            parts = line.split()
            if parts[0] == "default" or parts[0] == "0.0.0.0":
                ip = parts[1]
                break
        assert ip is not None, route_output
    cidrs = config["cidrs"]
    expose_ports = config["expose_ports"]

    # Start the sshuttle VPN-like thing:
    # XXX duplicates code in telepresence, remove duplication
    main_process = Popen([
        "sshuttle-telepresence", "-v", "--dns", "--method", "nat", "-e", (
            "ssh -oStrictHostKeyChecking=no -oUserKnownHostsFile=/dev/null " +
            "-F /dev/null"
        ), "--to-ns", "127.0.0.1:9053", "-r",
        "telepresence@{}:{}".format(ip, port)
    ] + cidrs)
    # Start the SSH tunnels to expose local services:
    subps = Subprocesses()
    runner = Runner.open("-", "kubectl", False)
    ssh = SSH(runner, port, ip)
    expose_local_services(subps, ssh, expose_ports)

    # Wait for everything to exit:
    wait_for_exit(runner, main_process, subps)


def wait():
    """Wait for proxying to be live."""
    start = time()
    while time() - start < 30:
        try:
            gethostbyname("hellotelepresence")
            sleep(1)  # just in case there's more to startup
            sys.exit(100)
        except gaierror:
            sleep(0.1)
    sys.exit("Failed to connect to proxy in remote cluster.")


if __name__ == '__main__':
    main()
