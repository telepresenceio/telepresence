#!/usr/bin/env python3
"""
Perform the steps required to build and deploy, but not release, a new version
of Telepresence.
"""

import json

from pathlib import Path
from shutil import rmtree
import subprocess

import package_linux

PROJECT = Path(__file__).absolute().resolve().parent.parent
DIST = PROJECT / "dist"
VENV_BIN = PROJECT / "virtualenv" / "bin"


def get_version():
    """Retrieve the current version number in the standard Python way"""
    version_bytes = subprocess.check_output(
        ["python3", "-Wignore", "setup.py", "--version"], cwd=str(PROJECT)
    )
    version = str(version_bytes, "utf-8").strip()
    return version


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
            dest.write("[Install][i] or [Upgrade][u].\n\n")
            dest.write("[i]: https://www.telepresence.io/reference/install\n")
            dest.write("[u]: https://www.telepresence.io/reference/upgrade\n")


def emit_machinery():
    """Copy scripts and data used by the release process"""
    machinery = [
        PROJECT / "packaging" / "homebrew-package.sh",
        PROJECT / "packaging" / "homebrew-formula.rb",
        PROJECT / "ci" / "release-in-docker.sh"
    ]
    for item in machinery:
        dest = DIST / item.name
        dest.write_bytes(item.read_bytes())
        dest.chmod(item.stat().st_mode)


def main():
    """
    Perform the steps required to build and deploy, but not release, a new
    version of Telepresence.
    """
    if DIST.exists():
        rmtree(str(DIST))
    DIST.mkdir(parents=True)
    version = get_version()
    release = "+" not in version # Is this a release version?
    if not release:
        # FIXME: Non-release versions should still yield... something.
        print("Version {} is not a release version".format(version))
        return

    emit_release_info(version)
    emit_announcement(version)
    emit_machinery()
    package_linux.main(version)


if __name__ == "__main__":
    main()
