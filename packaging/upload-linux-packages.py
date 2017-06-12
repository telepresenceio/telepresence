#!/usr/bin/env python3
"""
Upload packages to Bintray repositories.
"""

import sys
from pathlib import Path
from subprocess import run

PACKAGES = Path(__file__).absolute().parent / "out"


class Uploader(object):
    """
    Upload packages to Bintray.
    """

    def __init__(self, version):
        self.version = version

    def upload_ubuntu(self, release):
        """Upload a .deb for a specific release, e.g. 'xenial'."""
        file_path = PACKAGES / release / "telepresence_{}_amd64.deb".format(
            self.version
        )
        self._upload(file_path, "telepresence", "ubuntu/" + release)

    def upload_fedora(self, release):
        """Upload a .rpm for a specific Fedora release, e.g. '25'."""
        file_path = (
            PACKAGES / ("fedora-" + release) /
            "telepresence-{}-1.x86_64.rpm".format(self.version)
        )
        self._upload(file_path, "telepresence-rpm", "fedora/" + release)

    def upload_alpine(self, release):
        """Upload a .apk for a specific Alpine release, e.g. '3.5'."""
        file_path = (
            PACKAGES / ("alpine-" + release) /
            "telepresence_{}_noarch.apk".format(self.version)
        )
        self._upload(file_path, "telepresence", "alpine/" + release)

    def _upload(self, file_path, repository, distro):
        """Upload a file to a repository.

        :param file_path Path: Path to package to upload.
        :param repository str: Bintray repository.
        :param version str: Version of package.
        :param extra str: Extra options to attach to end of Bintray URL.
        """
        run([
            "package_cloud",
            "push",
            "datawireio/telepresence/" + distro,
            str(file_path),
        ],
            check=True)


def main(version):
    uploader = Uploader(version)
    for release in ["xenial", "yakkety", "zesty"]:
        uploader.upload_ubuntu(release)

    uploader.upload_fedora("25")

    for release in ["3.5", "3.6"]:
        uploader.upload_alpine(release)


if __name__ == '__main__':
    main(sys.argv[1])
