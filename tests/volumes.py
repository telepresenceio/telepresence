"""
Tests that accessing remote cluster from local container.

This module will be run inside a container. To indicate success it will exit
with code 113.
"""
import sys


def main():
    # Explicit volumes are exposed:
    with open("/podinfo/labels") as f:
        data = f.read()
        print(data)
        assert 'hello="monkeys"' in data
    # Implicitly added account volume is there too:
    with open("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt") as f:
        data = f.read()
        print(data)
        assert data.startswith("-----BEGIN CERT")
    # Exit with code indicating success:
    sys.exit(113)


if __name__ == '__main__':
    main()
