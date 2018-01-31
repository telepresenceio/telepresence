#!/usr/bin/env python3
"""
Delete old deployments and services with test-prefixed names. This is used to
clean up the Telepresence test cluster, as Telepresence tests currently leak.
"""

import argparse
import json
from datetime import datetime, timedelta, timezone
from subprocess import check_output
from typing import Dict, List


def get_kubectl() -> List[str]:
    """Get correct kubectl command"""
    k8s_namespace = str(
        check_output([
            "kubectl", "config", "view", "--minify=true",
            "-o=jsonpath={.contexts[0].context.namespace}"
        ]).strip(), "ascii"
    )
    if k8s_namespace:
        return ["kubectl", "--namespace", k8s_namespace]
    return ["kubectl"]


KUBECTL = get_kubectl()


def get_now() -> datetime:
    """Get current date/time in UTC"""
    return datetime.now(tz=timezone.utc)


def parse_k8s_timestamp(timestamp: str) -> datetime:
    """Get date/time in UTC from k8s timestamp"""
    fmt = "%Y-%m-%dT%H:%M:%SZ"
    naive = datetime.strptime(timestamp, fmt)
    return naive.replace(tzinfo=timezone.utc)


def get_kubectl_json(cmd: List[str]) -> Dict:
    """Call kubectl and parse resulting JSON"""
    output = str(check_output(KUBECTL + cmd + ["-o", "json"]), "utf-8")
    return json.loads(output)


def get_resources(kind: str, prefix="",
                  min_age=timedelta(seconds=0)) -> List[str]:
    """
    Return names of k8s resources with the given name prefix and minimum age
    """
    now = get_now()
    resources = get_kubectl_json(["get", kind])["items"]
    names = []
    for resource in resources:
        name = resource["metadata"]["name"]
        if kind == "svc" and name == "kubernetes":
            continue
        if not name.startswith(prefix):
            continue
        timestamp_str = resource["metadata"]["creationTimestamp"]
        timestamp = parse_k8s_timestamp(timestamp_str)
        age = now - timestamp
        if age < min_age:
            continue
        names.append("{}/{}".format(kind, name))
    return names


def seconds(value: str) -> timedelta:
    """Return a timedelta with the given number of seconds"""
    try:
        return timedelta(seconds=int(value))
    except ValueError:
        message = "Invalid age in seconds: {}".format(value)
        raise argparse.ArgumentTypeError(message)


def main():
    """Clean up the current Kubernetes cluster"""
    parser = argparse.ArgumentParser(
        allow_abbrev=False,  # can make adding changes not backwards compatible
        description=__doc__
    )
    parser.add_argument(
        "--prefix",
        default="testing-",
        help="prefix for resource name [testing-]"
    )
    parser.add_argument(
        "--min-age",
        type=seconds,
        default="86400",
        help="minimum age in seconds"
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="don't really delete anything"
    )
    args = parser.parse_args()

    names = [
        name for kind in ("svc", "deploy", "po")
        for name in get_resources(kind, args.prefix, args.min_age)
    ]
    if not names:
        print("Nothing to clean up.")
        return

    if args.dry_run:
        print("Would clean up:")
    else:
        print("Cleaning up:")

    for name in names:
        print(" {}".format(name))
    if not args.dry_run:
        check_output(KUBECTL + ["delete"] + names)


if __name__ == "__main__":
    main()
