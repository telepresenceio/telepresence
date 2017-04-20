#!/usr/bin/env python3

from shutil import rmtree
from subprocess import run
from pathlib import Path

THIS_DIRECTORY = Path(__file__).absolute().parent


def build_package(builder_image, package_type, version, dependencies):
    """
    Build a deb or RPM package using a fpm-within-docker Docker image.

    :param package_type str: "rpm" or "deb".
    :param version str: The package version.
    :param dependencies list: package names the resulting package should depend
        on.
    """
    run(["sudo", "docker", "run",  "--rm",
         "-e", "PACKAGE_VERSION=" + version,
         "-e", "PACKAGE_TYPE=" + package_type,
         "-v", "{}:/build-inside:ro".format(THIS_DIRECTORY),
         "-v", "{}:/source:ro".format(THIS_DIRECTORY.parent),
         "-v",  "{}/out:/out".format(THIS_DIRECTORY),
         "-w", "/build-inside",
         builder_image, "/build-inside/build-package.sh", *dependencies],
        check=True)


def main():
    out = THIS_DIRECTORY / "out"
    if out.exists():
        rmtree(str(out))
    out.mkdir()
    build_package("alanfranz/fwd-ubuntu-xenial:latest", "deb",
                  "0.32", ["torsocks", "python3", "openssh-client", "sshfs"])


if __name__ == '__main__':
    main()
