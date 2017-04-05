"""
Test that we can communicate with arbitrary hosts via the Kubernetes
cluster. For --docker-run this requires the --proxy argument.

We do this by setting an alias to a service in /etc/hosts on telepresence-k8s
pod. That means that connecting to the alias in the local computer should only
work if we're being proxied.
"""

from subprocess import check_call
import os
import sys
from urllib.request import urlopen
from urllib.error import HTTPError


def main():
    # Add alias analiaswedefine that points at nginx Service:
    check_call([
        "kubectl",
        "exec",
        "--container=" + os.environ["TELEPRESENCE_CONTAINER"],
        os.environ["TELEPRESENCE_POD"],
        "--",
        "/bin/sh",
        "-c",
        (
            r"""apk add --no-cache bind-tools; """ +
            r"""echo -e "\n$(host -t A {} | sed 's/.* \([.0-9]*\)/\1/')""" +
            r''' analiaswedefine\n" >> /etc/hosts; tail /etc/hosts'''
        ).format(sys.argv[1]),
    ])

    try:
        result = str(
            urlopen("http://analiaswedefine:80/", timeout=5).read(), "utf-8"
        )
        assert "nginx" in result
        # special code indicating success:
        raise SystemExit(113)
    except (HTTPError, AssertionError):
        raise SystemExit(3)


if __name__ == '__main__':
    main()
