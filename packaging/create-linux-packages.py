#!/usr/bin/env python3

"""
Create Ubuntu and Fedora packages in out/.

Usage:
create-linux-packages.py <release-version>
"""

import sys
from shutil import rmtree
from subprocess import run
from pathlib import Path

THIS_DIRECTORY = Path(__file__).absolute().parent


def build_package(builder_image, package_type, version, out_dir, dependencies):
    """
    Build a deb or RPM package using a fpm-within-docker Docker image.

    :param package_type str: "rpm" or "deb".
    :param version str: The package version.
    :param out_dir Path: Directory where package will be output.
    :param dependencies list: package names the resulting package should depend
        on.
    """
    run(["sudo", "docker", "run",  "--rm",
         "-e", "PACKAGE_VERSION=" + version,
         "-e", "PACKAGE_TYPE=" + package_type,
         "-v", "{}:/build-inside:ro".format(THIS_DIRECTORY),
         "-v", "{}:/source:ro".format(THIS_DIRECTORY.parent),
         "-v",  str(out_dir) + ":/out",
         "-w", "/build-inside",
         builder_image, "/build-inside/build-package.sh", *dependencies],
        check=True)


def test_package(distro_image, package_directory, install_command):
    """
    Test a package can be installed and Telepresence run.

    :param distro_image str: The Docker image to use to test the package.
    :param package_directory Path: local directory where the package can be
        found.
    :param install_command str: "deb", "rpm" or "apk".
    """
    if install_command == "deb":
        install = (
            "apt-get -q update && "
            "apt-get -q -y --no-install-recommends install gdebi-core && "
            "gdebi -n /packages/*.deb")
    elif install_command == "rpm":
        install = "dnf -y install /packages/*.rpm"
    elif install_command == "apk":
        install = (
            "apk update && "
            "apk add --allow-untrusted /packages/*.apk"
        )

    run(["sudo", "docker", "run", "--rm",
         "-v", "{}:/packages:ro".format(package_directory),
         distro_image,
         "sh", "-c", install + " && telepresence --version " +
         "&& sshuttle-telepresence --version"],
        check=True)


def main(version):
    out = THIS_DIRECTORY / "out"
    if out.exists():
        rmtree(str(out))
    out.mkdir()

    for ubuntu_distro in ["xenial", "yakkety", "zesty"]:
        distro_out = out / ubuntu_distro
        distro_out.mkdir()
        image = "alanfranz/fwd-ubuntu-{}:latest".format(ubuntu_distro)
        # At the moment we need custom image for zesty. This will be
        # unnecessary once
        # https://github.com/alanfranz/fpm-within-docker/pull/1 is merged:
        if ubuntu_distro == "zesty":
            image = "datawire/fpm-within-docker:zesty"

        build_package(image,
                      "deb",
                      version,
                      distro_out,
                      ["torsocks", "python3", "openssh-client", "sshfs"])
        test_package("ubuntu:" + ubuntu_distro, distro_out, "deb")

    for fedora_distro in ["25"]:
        distro_out = out / ("fedora-" + fedora_distro)
        distro_out.mkdir()
        build_package("alanfranz/fwd-fedora-{}:latest".format(fedora_distro),
                      "rpm",
                      version,
                      distro_out,
                      ["python3", "torsocks", "openssh-clients", "sshfs"])
        test_package("fedora:" + fedora_distro, distro_out, "rpm")

    # We need Alpine >3.5 for the torsocks package
    for alpine_distro in ["3.5", "3.6"]:
        # In the case of Alpine we need to build first an image that includes FPM
        image = "alpine-fpm:{}".format(alpine_distro)
        run(["sudo", "docker", "build", "--build-arg", "DISTRO={}".format(alpine_distro), "-f", str(THIS_DIRECTORY / "Dockerfile.alpine"), "-t", image, "."])

        distro_out = out / "alpine-{}".format(alpine_distro)
        distro_out.mkdir()

        build_package(image,
                      "apk",
                      version,
                      distro_out,
                      ["python3", "torsocks", "openssh-client", "sshfs"])
        test_package(image, distro_out, "apk")


if __name__ == '__main__':
    main(sys.argv[1])
