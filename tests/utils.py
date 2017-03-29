"""Utilities."""

import atexit
from pathlib import Path
import time
from subprocess import check_output, STDOUT, check_call

DIRECTORY = Path(__file__).absolute().parent
REVISION = str(check_output(["git", "rev-parse", "--short", "HEAD"]), "utf-8"
               ).strip()


def random_name():
    """Return a new name each time."""
    return "testing-{}-{}".format(REVISION, time.time()).replace(".", "-")


def telepresence_version():
    """Return the version of Telepresence we're testing."""
    return str(
        check_output(["telepresence", "--version"], stderr=STDOUT), "utf-8"
    ).strip()


def run_nginx():
    """Run nginx in Kuberentes; return Service name."""
    nginx_name = random_name()

    def cleanup():
        check_call([
            "kubectl", "delete", "--ignore-not-found",
            "service,deployment", nginx_name
        ])

    cleanup()
    atexit.register(cleanup)

    check_output([
        "kubectl",
        "run",
        "--generator",
        "deployment/v1beta1",
        nginx_name,
        "--image=nginx:alpine",
        "--port=80",
        "--expose",
    ])
    time.sleep(30)  # kubernetes is speedy
    return nginx_name
