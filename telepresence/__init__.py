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
Telepresence: local development environment for a remote Kubernetes cluster.
"""

import os
from pathlib import Path

# Version number computed by Versioneer. See _version.py for info.
from ._version import get_versions  # type: ignore

__version__ = get_versions()['version']  # type: str
del get_versions

# Use the most recent released image version. Override below:
try:
    image_version = __version__[:__version__.index("-")]
except ValueError:
    image_version = __version__

# Test runs can override version so we use specific custom Docker images:
version_override = False
if os.environ.get("TELEPRESENCE_VERSION") is not None:
    image_version = os.environ["TELEPRESENCE_VERSION"]
    version_override = True

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")
TELEPRESENCE_LOCAL_IMAGE = "{}/telepresence-local:{}".format(
    REGISTRY, image_version
)
TELEPRESENCE_REMOTE_IMAGE = "{}/telepresence-k8s:{}".format(
    REGISTRY, image_version
)
TELEPRESENCE_REMOTE_IMAGE_PRIV = "{}/telepresence-k8s-priv:{}".format(
    REGISTRY, image_version
)
TELEPRESENCE_REMOTE_IMAGE_OCP = "{}/telepresence-ocp:{}".format(
    REGISTRY, image_version
)

# This path points to one of
# - the telepresence executable zip file, for an installed telepresence
# - the directory that contains the telepresence Python package (which is
#   a directory containing this __init__.py file)
TELEPRESENCE_BINARY = Path(__file__).parents[1].resolve()

from ._version import get_versions
__version__ = get_versions()['version']
del get_versions
