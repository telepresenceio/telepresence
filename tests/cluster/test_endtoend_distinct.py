"""
A module for end-to-end Telepresence tests that don't use ``with_probe``.

This is more than redecorating for the test suite.  ``with_probe`` creates a
module-scope pytest fixture.  Tests defined inside the same module will share
their ``Probe`` instances.  Also, ``with_probe``-managed ``Probe`` instances
won't be cleaned up until the entire module has been executed.

Put tests in this module if they don't use ``with_probe`` but do use
``Probe``.
"""

import subprocess
from shutil import which
from time import sleep, time

import pytest

from .parameterize_utils import (
    INJECT_TCP_METHOD, NEW_DEPLOYMENT_OPERATION_GETTER, NoTaggedValue, Probe
)


def test_disconnect(request):
    """
    Telepresence exits with code 255 if its connection to the cluster is lost.

    FIXME: This is the standard failure exit code. We should decide if we want
    to go back to Telepresence indicating disconnect in a manner that's
    distinguishable from other failures.
    """
    # Avoid using the Probe fixture because it is scoped for multi-test use to
    # allow a Telepresence session to be used by multiple tests.  This test is
    # going to ruin its Telepresence session.  We don't want that to affect
    # other tests.
    #
    # Just pick a semi-arbitrary Probe configuration.  We do need to have
    # kubectl available in the Telepresence execution context for
    # ``disconnect_telepresence`` to work, though.
    probe = Probe(
        request, INJECT_TCP_METHOD, NEW_DEPLOYMENT_OPERATION_GETTER()
    )
    request.addfinalizer(probe.cleanup)

    probe_result = probe.result()
    disconnect_telepresence(
        probe_result,
        probe_result.deployment_ident.namespace,
    )
    for i in range(30):
        returncode = probe_result.telepresence.poll()
        if returncode is not None:
            break
        sleep(1.0)

    with pytest.raises(NoTaggedValue):
        # Read so we get the last bit of Telepresence output logged.  There is
        # no tagged value from this command so the read will fail but that's
        # okay.
        probe_result.read()

    assert returncode == 255, (
        "Telepresence returncode did not indicate disconnection detected."
    )


def disconnect_telepresence(probe_result, namespace):
    probe_result.write("disconnect-telepresence " + namespace)


def test_docker_mount(request):
    if which("docker") is None:
        pytest.skip("Docker unavailable")
        # not reached
    mount_dir = "/test{}".format(int(time()))
    filename = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
    script = "[ $TELEPRESENCE_ROOT == {} ]".format(mount_dir)
    script += " && cat {}{}".format(mount_dir, filename)
    args = [
        "telepresence", "--logfile=-", "--docker-mount", mount_dir,
        "--docker-run", "--rm", "alpine:3.10", "sh", "-x", "-c", script
    ]
    subprocess.check_call(args)
