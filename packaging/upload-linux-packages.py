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
    def __init__(self, version, api_key):
        self.version = version
        self.api_key = api_key

    def upload_ubuntu(self, release):
        """Upload a .deb for a specific release, e.g. 'xenial'."""
        file_path = PACKAGES / release / "telepresence_{}_amd64.deb".format(
            self.version)
        self._upload(
            file_path, "telepresence",
            ";deb_distribution={}".format(release) +
            ";deb_component=main;deb_architecture=amd64")

    def upload_fedora(self, release):
        """Upload a .rpm for a specific Fedora release, e.g. 'fedora-25'."""
        # XXX haven't figured out how to differentiate different Fedora
        # releases' packages, so need to get back to this in later branch.
        # Disabled for now.
        return
        file_path = PACKAGES / release / "telepresence-{}-1.x86_64.rpm".format(
            self.version)
        self._upload(file_path, "telepresence-rpm", "")

    def _upload(self, file_path, repository, extra):
        """Upload a file to a repository.

        :param file_path Path: Path to package to upload.
        :param repository str: Bintray repository.
        :param version str: Version of package.
        :param extra str: Extra options to attach to end of Bintray URL.
        """
        run(["curl", "-T", str(file_path),
             "-udatawireio:" + self.api_key,
             "https://api.bintray.com/content/datawireio/" +
             "{}/telepresence/{}/{}{}?publish=1".format(
                 repository, file_path.basename, self.version, extra)],
            check=True)


def main(version, api_key):
    uploader = Uploader(version, api_key)
    for release in ["xenial", "yakkety", "zesty"]:
        uploader.upload_ubuntu(release)
    # uploader.upload_fedora("fedora-25")


if __name__ == '__main__':
    main(sys.argv[1], sys.argv[2])
