import socket
from time import time
from typing import List

import os


def random_name() -> str:
    """Return a random name for a container."""
    return "telepresence-{}-{}".format(time(), os.getpid()).replace(".", "-")


def find_free_port() -> int:
    """
    Find a port that isn't in use.

    XXX race condition-prone.
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        s.bind(("", 0))
        return s.getsockname()[1]
    finally:
        s.close()


def get_resolv_conf_namservers() -> List[str]:
    """Return list of namserver IPs in /etc/resolv.conf."""
    result = []
    with open("/etc/resolv.conf") as f:
        for line in f:
            parts = line.lower().split()
            if len(parts) >= 2 and parts[0] == 'nameserver':
                result.append(parts[1])
    return result


def get_alternate_nameserver() -> str:
    """Get a public nameserver that isn't in /etc/resolv.conf."""
    banned = get_resolv_conf_namservers()
    # From https://www.lifewire.com/free-and-public-dns-servers-2626062 -
    public = [
        "8.8.8.8", "8.8.4.4", "216.146.35.35", "216.146.36.36", "209.244.0.3",
        "209.244.0.4", "64.6.64.6", "64.6.65.6"
    ]
    for nameserver in public:
        if nameserver not in banned:
            return nameserver
    raise RuntimeError("All known public nameservers are in /etc/resolv.conf.")
