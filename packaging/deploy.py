#!/usr/bin/env python3
# Copyright 2018 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""
Perform the steps required to build and deploy, but not release, a new version
of Telepresence.
"""

import json

from pathlib import Path
from shutil import rmtree
import subprocess

import package_linux
from container import Container

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
    --key telepresence/{version}/telepresence \\
    --body {executable_path.name}
aws s3api put-object \\
    --bucket datawire-static-files \\
    --key telepresence/{version}/sshuttle-telepresence \\
    --body {sshuttle_executable_path.name}
"""
_S3_RELEASE = """
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
    executable_path = DIST / "telepresence"
    sshuttle_executable_path = DIST / "sshuttle-telepresence"
    s3_uploader_path = DIST / "s3_uploader.sh"
    with s3_uploader_path.open(mode="w", encoding="UTF-8") as out:
        out.write(_S3_UPLOADER.format(**locals()))
    s3_uploader_path.chmod(0o775)

    if "-" in version:  # Detect that this is not a release version
        return

    release_version_path = DIST / "release_version.txt"
    release_version_path.write_text(version)

    scout_info = {
        "application": "telepresence",
        "latest_version": version,
        "notices": notices or []
    }
    scout_blob_path = DIST / "scout_blob.json"
    scout_blob_path.write_text(json.dumps(scout_info))

    with s3_uploader_path.open(mode="a", encoding="UTF-8") as out:
        out.write(_S3_RELEASE.format(**locals()))


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
            dest.write("# Telepresence {}\n".format(version))
            for line in source:
                if line.startswith("#### "):
                    break
                dest.write(line)
            dest.write(
                "[Install](https://www.telepresence.io/reference/install) "
                "or "
                "[Upgrade](https://www.telepresence.io/reference/upgrade).\n"
            )


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


def build_executables():
    """Build Telepresence binaries in Docker and copy them to dist"""
    con = Container("python:3.6-alpine")
    con.execute_sh("apk update -q")
    con.execute_sh("apk add -q git")
    con.execute_sh("mkdir /source")
    con.copy_to(".", "/source")
    con.execute_sh("rm -r /source/dist")
    con.execute_sh(
        "python3 packaging/build-telepresence.py dist/telepresence",
        cwd="/source"
    )
    con.execute_sh("python3 packaging/build-sshuttle.py", cwd="/source")
    con.copy_from("/source/dist/telepresence", str(DIST))
    con.copy_from("/source/dist/sshuttle-telepresence", str(DIST))


def main():
    """
    Perform the steps required to build and deploy, but not release, a new
    version of Telepresence.
    """
    if DIST.exists():
        rmtree(str(DIST))
    DIST.mkdir(parents=True)
    version = get_version()
    release = "-" not in version  # Is this a release version?
    build_executables()
    emit_release_info(version)
    package_linux.main(version)

    if not release:
        print("Version {} is not a release version".format(version))
        return

    emit_announcement(version)
    emit_machinery()


if __name__ == "__main__":
    main()
