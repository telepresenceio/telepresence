"""
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
"""

import sys
import os
import json
from subprocess import check_output


def main():
    config = json.loads(sys.argv[1])
    port = config["port"]
    if "ip" in config:
        # Typically host is macOS:
        ip = config["ip"]
    else:
        # Typically host is Linux, use default route:
        for line in str(check_output(["route"]), "ascii").splitlines():
            parts = line.split()
            if parts[0] == "default":
                ip = parts[1]
                break
    cidrs = config["cidrs"]
    os.execl(
        "sshuttle-telepresence", "-v", "--dns", "--method", "nat", "-e", (
            "ssh -oStrictHostKeyChecking=no -oUserKnownHostsFile=/dev/null " +
            "-F /dev/null"
        ), "-r", "telepresence@{}:{}".format(ip, port), *cidrs
    )


if __name__ == '__main__':
    main()
