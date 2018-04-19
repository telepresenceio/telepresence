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

import ssl
import sys

from subprocess import CalledProcessError
from typing import Optional
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

from shutil import which

from telepresence.runner import Runner


def require_command(
    runner: Runner, command: str, message: Optional[str] = None
):
    if message is None:
        message = "Please install " + command
    try:
        runner.get_output(["which", command])
    except CalledProcessError as e:
        sys.stderr.write(message + "\n")
        sys.stderr.write(
            '(Ran "which {}" to check in your $PATH.)\n'.format(command)
        )
        sys.stderr.write(
            "See the documentation at https://telepresence.io "
            "for more details.\n"
        )
        raise SystemExit(1)


def kubectl_or_oc(server: str) -> str:
    """
    Return "kubectl" or "oc", the command-line tool we should use.

    :param server: The URL of the cluster API server.
    """
    if which("oc") is None:
        return "kubectl"
    # We've got oc, and possibly kubectl as well. We only want oc for OpenShift
    # servers, so check for an OpenShift API endpoint:
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        with urlopen(server + "/version/openshift", context=ctx) as u:
            u.read()
    except (URLError, HTTPError):
        return "kubectl"
    else:
        return "oc"
