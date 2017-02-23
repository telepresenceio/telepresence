#!/usr/bin/env python

from __future__ import print_function

from subprocess import check_output
from json import loads

# Try to work with Python 2 and 3:
try:
    unicode
except NameError:
    unicode = str


def _get_services():
    """Return iterable of (name, namespace, ip, port)."""
    services_json = check_output(["kubectl", "get", "service", "-o", "json"])
    services_json = loads(unicode(services_json, "utf-8"))
    for service in services_json["items"]:
        # XXX will break for some kinds of services... brittle!
        # XXX multiple ports
        yield (service["metadata"]["name"], service["metadata"]["namespace"],
               service["spec"]["clusterIP"], service["spec"]["ports"][0]["port"])


def get_services(namespace="default"):
    """Return iterable of (name, ip, port), sorted."""
    for name, ns, ip, port in sorted(_get_services()):
        if ns == namespace:
            yield name, ip, port


def get_env_variables(services):
    """Generate environment variables that match kubernetes."""
    # XXX need to transform to localhost or container IP. And ensure uniqueness
    # of ports.
    for name, ip, port in services:
        port = str(port)
        name = name.replace("-", "_").upper()
        # XXX will be wrong for UDP
        full_address = "tcp://{}:{}".format(ip, port)
        yield name + "_SERVICE_HOST", ip
        yield name + "_SERVICE_PORT", port
        yield name + "_PORT", full_address
        port_name = name + "_PORT_" + port + "_TCP"
        yield port_name, full_address
        # XXX will break for UDP
        yield port_name + "_PROTO", "tcp"
        yield port_name + "_PORT", port
        yield port_name + "_HOST", ip


def main():
    services = list(get_services())
    with open("k8s.env", "w") as f:
        for key, value in get_env_variables(services):
            f.write("{}={}\n".format(key, value))
    print("Please pass --env-file=k8s.env to docker run.")


if __name__ == '__main__':
    main()
