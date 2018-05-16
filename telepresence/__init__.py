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

# Don't modify next line without modifying corresponding line in
# .bumpversion.cfg:
__version__ = "0.88"
# Test runs can override version so we use specific custom Docker images:
if os.environ.get("TELEPRESENCE_VERSION") is not None:
    __version__ = os.environ["TELEPRESENCE_VERSION"]

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")
TELEPRESENCE_LOCAL_IMAGE = "{}/telepresence-local:{}".format(
    REGISTRY, __version__
)
TELEPRESENCE_REMOTE_IMAGE = "{}/telepresence-k8s:{}".format(
    REGISTRY, __version__
)
TELEPRESENCE_REMOTE_IMAGE_PRIV = "{}/telepresence-k8s-priv:{}".format(
    REGISTRY, __version__
)
