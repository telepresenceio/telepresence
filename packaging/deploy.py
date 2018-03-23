#!/usr/bin/env python3
"""
Perform the steps required to build and deploy, but not release, a new version
of Telepresence.
"""

import json

from pathlib import Path
from shutil import rmtree

import package_linux

PROJECT = Path(__file__).absolute().resolve().parent.parent
DIST = PROJECT / "dist"
VENV_BIN = PROJECT / "virtualenv" / "bin"


def get_version():
    """Retrieve the current version number"""
    cfg = PROJECT / ".bumpversion.cfg"
    for line in cfg.open():
        if line.startswith("current_version = "):
            current = line.split(" = ", 1)[1]
            break
    else:
        exit("Could not find version number in {}".format(cfg))
    return current.strip()


_S3_UPLOADER = """#!/bin/bash
set -e
cd "$(dirname "$0")"
export AWS_DEFAULT_REGION=us-east-1
aws s3api put-object \\
    --bucket datawire-static-files \\
    --key telepresence/stable.txt \\
    --body {release_version_path.name}
aws s3api put-object \\
    --bucket scout-datawire-io \\
    --key telepresence/app.json \\
    --body {scout_blob_path.name}
"""


def emit_release_info(version, notices=None):
    """Generate files in dist that handle scout and release info"""
    release_version_path = DIST / "release_version.txt"
    release_version_path.write_text(version)

    scout_info = {
        "application": "telepresence",
        "latest_version": version,
        "notices": notices or []
    }
    scout_blob_path = DIST / "scout_blob.json"
    scout_blob_path.write_text(json.dumps(scout_info))

    s3_uploader_path = DIST / "s3_uploader.sh"
    s3_uploader_path.write_text(_S3_UPLOADER.format(**locals()))
    s3_uploader_path.chmod(0o775)


def emit_announcement(version):
    """Extract the latest changelog entry as a release announcement."""
    changelog = PROJECT / "docs" / "reference" / "changelog.md"
    announcement = DIST / "announcement.md"
    with announcement.open("w") as dest:
        with changelog.open() as source:
            expected = "#### {} (".format(version)
            for line in source:
                if line.startswith(expected):
                    break
            else:
                raise RuntimeError("Start not found [{}]".format(expected))
            dest.write("Announcing\n")
            dest.write("# Telepresence {}\n".format(version))
            for line in source:
                if line.startswith("#### "):
                    break
                dest.write(line)
            dest.write("See how to [install][i] or [upgrade][u].\n\n")
            dest.write("[i]: https://www.telepresence.io/reference/install\n")
            dest.write("[u]: https://www.telepresence.io/reference/upgrade\n")


def main():
    """
    Perform the steps required to build and deploy, but not release, a new
    version of Telepresence.
    """
    if DIST.exists():
        rmtree(str(DIST))
    DIST.mkdir(parents=True)
    version = get_version()
    emit_release_info(version)
    emit_announcement(version)
    package_linux.main(version)


if __name__ == "__main__":
    main()
