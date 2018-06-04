"""
Telepresence: local development environment for a remote Kubernetes cluster.
"""

import os
import re
from setuptools import setup

import versioneer


def get_version(filename="telepresence/__init__.py"):
    """Parse out version info"""
    base_dir = os.path.dirname(os.path.abspath(__file__))
    with open(os.path.join(base_dir, filename)) as initfile:
        for line in initfile.readlines():
            match = re.match("__version__ *= *['\"](.*)['\"]", line)
            if match:
                return match.group(1)


setup(
    name="telepresence",
    version=versioneer.get_version(),
    cmdclass=versioneer.get_cmdclass(),
    description=__doc__,
    packages=["telepresence"],
    entry_points={
        "console_scripts": [
            "telepresence = telepresence.main:run_telepresence"
        ]
    },
    url="https://www.telepresence.io"
)
