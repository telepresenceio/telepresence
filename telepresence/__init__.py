"""
Telepresence: local development environment for a remote Kubernetes cluster.
"""

import os

# Don't modify next line without modifying corresponding line in
# .bumpversion.cfg:
__version__ = "0.79"
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
