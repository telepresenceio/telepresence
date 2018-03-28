#!/usr/bin/env python3
"""
Create Ubuntu and Fedora packages in dist.

Usage:
create-linux-packages.py <release-version>
"""

import sys
from shutil import rmtree
from subprocess import run
from pathlib import Path

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


def build_package(builder_image, package_type, version, out_dir, dependencies):
    """
    Build a deb or RPM package using a fpm-within-docker Docker image.

    :param package_type str: "rpm" or "deb".
    :param version str: The package version.
    :param out_dir Path: Directory where package will be output.
    :param dependencies list: package names the resulting package should depend
        on.
    """
    run([
        "docker", "run", "--rm", "-e", "PACKAGE_VERSION=" + version, "-e",
        "PACKAGE_TYPE=" + package_type, "-v",
        "{}:/build-inside:ro".format(THIS_DIRECTORY), "-v",
        "{}:/source:ro".format(THIS_DIRECTORY.parent), "-v",
        str(out_dir) + ":/out", "-w", "/build-inside", builder_image,
        "/build-inside/build-package.sh", *dependencies
    ],
        check=True)


def test_package(distro_image, package_directory, install_command):
    """
    Test a package can be installed and Telepresence run.

    :param distro_image str: The Docker image to use to test the package.
    :param package_directory Path: local directory where the package can be
        found.
    :param install_command str: commands to install packages in /packages
    """
    command = """
        set -e
        {}
        telepresence --version
        stamp-telepresence --version
        sshuttle-telepresence --version
    """.format(install_command)
    run([
        "docker", "run", "--rm",
        "-v={}:/packages:ro".format(package_directory), distro_image, "sh",
        "-c", command
    ],
        check=True)


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
    uploads = []
    for system, release, package_type, dependencies, install_command in distros:
        name = "{}-{}".format(system, release)
        distro_out = DIST / name
        if distro_out.exists():
            rmtree(str(distro_out))
        distro_out.mkdir(parents=True)

        show_banner("Build {}".format(name))
        image = "alanfranz/fpm-within-docker:{}".format(name)
        build_package(image, package_type, version, distro_out, dependencies)

        show_banner("Test {}".format(name))
        image = "{}:{}".format(system, release)
        test_package(image, distro_out, install_command)

        package = next(distro_out.glob("*"))
        rel_package = package.relative_to(DIST)
        uploads.extend(get_upload_commands(system, release, rel_package))

    upload_script = DIST / "upload_linux_packages.sh"
    with upload_script.open("w") as f:
        f.write("#!/bin/sh\n\n")
        f.write("set -e\n\n")
        f.write('cd "$(dirname "$0")"\n')
        f.write("\n".join(uploads))
        f.write("\n")
    upload_script.chmod(0o775)


if __name__ == '__main__':
    main(sys.argv[1])
