import os
from sys import executable

from json import (
    dumps,
)

from subprocess import (
    check_output,
)

from .rwlock import RWLock

from .utils import (
    KUBECTL,
    telepresence_version,
)


# Some Telepresence configurations take over the machine's whole network
# stack.  We must avoid concurrency when running those tests.  The network
# lock gives us a way to do that.  Tests with an isolated network impact
# (inject-tcp, container) will do a read-acquire on this lock.  Tests with a
# global network impact (vpn-tcp) will do a write-acquire on this lock.
network = RWLock()

REGISTRY = os.environ.get("TELEPRESENCE_REGISTRY", "datawire")


class _ContainerMethod(object):
    name = "--method=container"

    def lock(self):
        network.lock_read()


    def unlock(self):
        network.unlock_read()


    def telepresence_args(self, probe):
        return [
            "--method", "container",
            "--docker-run",
            "--volume", "{}:/probe.py".format(probe),
            "python:3-alpine",
            "python", "/probe.py",
        ]


class _InjectTCPMethod(object):
    name = "--method=inject-tcp"

    def lock(self):
        network.lock_read()


    def unlock(self):
        network.unlock_read()


    def telepresence_args(self, probe):
        return [
            "--method", "inject-tcp",
            "--run", executable, probe,
        ]



class _VPNTCPMethod(object):
    name = "--method=vpn-tcp"

    def lock(self):
        network.lock_write()


    def unlock(self):
        network.unlock_write()


    def telepresence_args(self, probe):
        return [
            "--method", "vpn-tcp",
            "--run", executable, probe,
        ]


class _ExistingDeploymentOperation(object):
    def __init__(self, swap):
        self.swap = swap
        if swap:
            self.name = "--swap-deployment"
        else:
            self.name = "--deployment"


    def prepare_deployment(self, deployment_ident, environ):
        if self.swap:
            image = "openshift/hello-openshift"
        else:
            image = "{}/telepresence-k8s:{}".format(
                REGISTRY,
                telepresence_version(),
            )
        create_deployment(deployment_ident, image, environ)


    def telepresence_args(self, deployment_ident):
        if self.swap:
            option = "--swap-deployment"
        else:
            option = "--deployment"
        return [
            "--namespace", deployment_ident.namespace,
            option, deployment_ident.name,
        ]



class _NewDeploymentOperation(object):
    name = "--new-deployment"

    def prepare_deployment(self, deployment_ident, environ):
        pass

    def telepresence_args(self, deployment_ident):
        return [
            "--namespace", deployment_ident.namespace,
            "--new-deployment", deployment_ident.name,
        ]



def create_deployment(deployment_ident, image, environ):
    def env_arguments(environ):
        return list(
            "--env={}={}".format(k, v)
            for (k, v)
            in environ.items()
        )
    deployment = dumps({
        "kind": "Deployment",
        "apiVersion": "extensions/v1beta1",
        "metadata": {
            "name": deployment_ident.name,
            "namespace": deployment_ident.namespace,
        },
        "spec": {
            "replicas": 2,
            "template": {
                "metadata": {
                    "labels": {
                        "name": deployment_ident.name,
                        "telepresence-test": deployment_ident.name,
                    },
                },
                "spec": {
                    "containers": [{
                        "name": "hello",
                        "image": image,
                        "env": list(
                            {"name": k, "value": v}
                            for (k, v)
                            in environ.items()
                        ),
                    }],
                },
            },
        },
    })
    check_output([KUBECTL, "create", "-f", "-"], input=deployment.encode("utf-8"))
