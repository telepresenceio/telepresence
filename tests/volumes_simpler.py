"""
Tests that accessing remote cluster from local container.

Unlike volumes.py, this doesn't assume an explicit volume was configured.

This module will be run inside a container. To indicate success it will exit
with code 113.
"""
import sys
import os


def main():
    root = os.environ["TELEPRESENCE_ROOT"]
    # Implicitly added account volume is there:
    with open(
        os.path.
        join(root, "var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
    ) as f:
        data = f.read()
        print(data)
        assert data.startswith("-----BEGIN CERT")
    # Exit with code indicating success:
    sys.exit(113)


if __name__ == '__main__':
    main()
