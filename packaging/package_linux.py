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
Create Ubuntu and Fedora packages in dist.

Usage:
create-linux-packages.py <release-version>
"""

import sys
from pathlib import Path
from subprocess import check_call
from typing import List

from container import Container
from distros import distros

THIS_DIRECTORY = Path(__file__).absolute().resolve().parent
DIST = THIS_DIRECTORY.parent / "dist"


def show_banner(text, char="=", width=79):
    """
    Make it easy to show what's going on
    """
    res = char * 2 + " " + text
    remaining = width - len(res) - 1
    if remaining > 0:
        res += " " + char * remaining
    print("\n" + res + "\n")


def prep_to_build() -> Container:
    """
    Prepare container to build packages
    """
    con = Container("alpine:3.7")
    con.execute_sh("apk update -q")
    con.execute_sh("apk add -q alpine-sdk dpkg-dev rpm-dev ruby ruby-dev")
    con.execute_sh("gem install -q --no-ri --no-rdoc fpm")
    con.copy_to(str(DIST / "telepresence"), "/usr/bin")
    con.copy_to(str(DIST / "sshuttle-telepresence"), "/usr/libexec")
    return con


def build_package(
    con: Container, name: str, version: str, dependencies: List[str],
    package_type: str
) -> str:
    """
    Build a package in the prepared build container
    """
    fpm_header = [
        "fpm",
        "--name=telepresence",
        "--version={}".format(version),
        "--description=Local development for a remote Kubernetes cluster.",
        "--input-type=dir",
    ]
    fpm_deps = ["--depends={}".format(dep) for dep in dependencies]
    fpm_type = ["--output-type={}".format(package_type)]
    fpm_trailer = [
        "/usr/bin/telepresence",
        "/usr/libexec/sshuttle-telepresence",
    ]
    target_path = DIST / name
    target_path.mkdir()

    pkg_dir = "/" + name
    con.execute_sh("mkdir {}".format(pkg_dir))
    con.execute(fpm_header + fpm_deps + fpm_type + fpm_trailer, cwd=pkg_dir)
    pkg_name = con.execute_sh("ls", cwd=pkg_dir).strip()
    con.copy_from(str(Path(pkg_dir) / pkg_name), str(target_path))

    rel_package = str(Path(name) / pkg_name)
    return rel_package


def test_package(image: str, package: Path, install_cmd: str):
    """
    Test a package can be installed and Telepresence run.
    """
    con = Container(image)
    con.execute_sh("mkdir /packages")
    con.copy_to(str(package), "/packages")
    package_path = "/packages/{}".format(package.name)
    command = "set -e\n{}".format(install_cmd).format(package_path)
    con.execute(["sh", "-c", command])
    con.execute_sh("python3 --version")
    con.execute_sh("telepresence --version")
    con.execute_sh("/usr/libexec/sshuttle-telepresence --version")


def get_upload_commands(system, release, package):
    """Returns the required package_cloud commands to upload this package"""
    repos = ["datawireio/stable", "datawireio/telepresence"]
    res = []
    for repo in repos:
        res.append(
            "package_cloud push {}/{}/{} {}".format(
                repo, system, release, package
            )
        )
    return res


def main(version):
    """Create Linux packages"""
    show_banner("Pulling images...")
    for system, release, _, _, _ in distros:
        check_call(["docker", "pull", "{}:{}".format(system, release)])

    show_banner("Building packages...")
    con = prep_to_build()
    uploads = []
    for system, release, package_type, dependencies, install_cmd in distros:
        name = "{}-{}".format(system, release)
        show_banner("Build {}".format(name))
        rel_package = build_package(
            con, name, version, dependencies, package_type
        )
        package = DIST / rel_package
        show_banner("Test {}".format(name))
        image = "{}:{}".format(system, release)
        test_package(image, package, install_cmd)
        rel_package = package.relative_to(DIST)
        uploads.extend(get_upload_commands(system, release, rel_package))

    upload_script = Path(DIST / "upload_linux_packages.sh")
    with upload_script.open("w") as f:
        f.write("#!/bin/sh\n\n")
        f.write("set -e\n\n")
        f.write('cd "$(dirname "$0")"\n')
        f.write("\n".join(uploads))
        f.write("\n")
    upload_script.chmod(0o775)


if __name__ == '__main__':
    main(sys.argv[1])
