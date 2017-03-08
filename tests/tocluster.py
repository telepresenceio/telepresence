"""
Tests that accessing remote cluster from local container.

This module will be run inside a container. To indicate success it will print
"SUCCESS!".
"""

import os
import ssl
from urllib.request import urlopen
from urllib.error import HTTPError


def check_kubernetes_api_url(url):
    # XXX Perhaps we can have a more robust test by starting our own service.
    print("Retrieving " + url)
    context = ssl._create_unverified_context()
    try:
        urlopen(url, timeout=5, context=context)
    except HTTPError as e:
        # Unauthorized (401) is default response code for Kubernetes API
        # server.
        if e.code == 401:
            return
        raise


port = os.environ["KUBERNETES_SERVICE_PORT"]
# Check environment variable based service lookup:
check_kubernetes_api_url("https://{}:{}/".format(
    os.environ["KUBERNETES_SERVICE_HOST"],
    port, ))
# Check hostname lookup, both partial and full:
check_kubernetes_api_url("https://kubernetes:{}/".format(port))
check_kubernetes_api_url(
    "https://kubernetes.default.svc.cluster.local:{}/".format(port))

print("SUCCESS!")
