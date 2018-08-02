"""
Telepresence: local development environment for a remote Kubernetes cluster.
"""

from setuptools import setup, find_packages

import versioneer

setup(
    name="telepresence",
    version=versioneer.get_version(),
    cmdclass=versioneer.get_cmdclass(),
    description=__doc__,
    packages=find_packages(include=("telepresence*",)),
    entry_points={
        "console_scripts": [
            "telepresence = telepresence.main:run_telepresence"
        ]
    },
    url="https://www.telepresence.io"
)
