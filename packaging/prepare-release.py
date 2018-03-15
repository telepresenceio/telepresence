#!/usr/bin/env python3
"""
Perform the steps required to build and deploy, but not release, a new version
of Telepresence.

Prep for package/release
- changelog
- version and tag:
  ./virtualenv/bin/bumpversion --verbose --list minor
- docker build/push:
  ./build --registry datawire --build-and-push --version-suffix="" --no-tests
- run tests
- Display change log diff

Package
- Make packages for Linux:
  ./packaging/create-linux-packages.py <new version number>
- Build Scout blob (this program)
- Build Gitter announcement (this program)

Release
- Upload linux packages to package cloud:
  rvm install 2.1 (from ci/release.sh)
  gem install package_cloud (from ci/release.sh)
  ./dist/upload_linux_packages.sh
- Update Homebrew package:
  (GitHub key stuff from ci/release.sh)
  ./packaging/homebrew-package.sh
- test linux packages remotely
- Push scout blobs
- Post on Gitter
"""

import json
import subprocess

from pathlib import Path
from shutil import rmtree

PROJECT = Path(__file__).absolute().resolve().parent.parent
DIST = PROJECT / "dist"
VENV_BIN = PROJECT / "virtualenv" / "bin"


def get_versions():
    """Retrieve the current and new version numbers"""
    bumpversion = str(VENV_BIN / "bumpversion")
    command = [bumpversion] + "--list --dry-run --allow-dirty minor".split()
    data = subprocess.check_output(command)
    lines = data.decode("utf-8").splitlines()
    current = new = None
    for line in lines:
        if line.startswith("current_version="):
            current = line.split("=", 1)[1]
        elif line.startswith("new_version="):
            new = line.split("=", 1)[1]
    assert current, lines
    assert new, lines
    return current, new


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
    current, new = get_versions()
    emit_release_info(new)
    emit_announcement(new)
    print("Changelog diff:")
    print("git diff -b {}..HEAD docs/reference/changelog.md".format(current))


if __name__ == "__main__":
    main()
