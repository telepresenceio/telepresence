"""
Tests that access to a hostname supplied via ``--also-proxy`` is proxied.

This module will indicate success by exiting with code 113.
"""

import sys
from json import loads
from urllib.request import Request, urlopen
from traceback import print_exception

def handle_error(type, value, traceback):
    print_exception(type, value, traceback, file=sys.stderr)
    raise SystemExit(3)


def main():
    # make sure exceptions cause exit:
    sys.excepthook = handle_error

    # Our own address, an address not on the cluster, from which the request
    # should not originate.
    local_ip = sys.argv[1]

    # Issue the request to a specific httpbin IP as a work-around for
    # <https://github.com/datawire/telepresence/issues/379>.  We must use http
    # to avoid SNI problems.
    url = "http://23.23.209.130/ip"
    # And we must specify the host header to avoid vhost problems.
    request = Request(url, None, {"Host": "httpbin.org"})

    print("Retrieving {}".format(url))
    result = str(urlopen(request, timeout=5).read(), "utf-8")
    print("Got {} from webserver.".format(repr(result)))
    request_ip = loads(result)["origin"]

    assert request_ip !=  local_ip, (
        "Found local IP ({}) as request origin instead of cluster IP.".format(
            local_ip,
        )
    )

    # Exit with code indicating success:
    sys.exit(113)

if __name__ == '__main__':
    main()
