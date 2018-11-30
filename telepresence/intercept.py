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

import json
from subprocess import DEVNULL, Popen
from urllib.error import HTTPError
from urllib.request import urlopen, Request

from telepresence.cli import crash_reporting, PortMapping
from telepresence.connect import connect
from telepresence.proxy import get_remote_info
from telepresence.runner import wait_for_exit
from telepresence.utilities import find_free_port


def command(runner, args):
    with runner.cleanup_handling(), crash_reporting(runner):
        # Process arguments
        name = args.name or runner.session_id
        local_port = args.port
        deployment = args.deployment
        patterns = [
            dict(name=header, regex_match=pattern)
            for header, pattern in args.match
        ]

        # Inform the user
        runner.show("Setting up intercept session {}".format(name))
        runner.show("Intercepting requests to {}".format(deployment))
        runner.show("and redirecting them to localhost:{}".format(local_port))
        runner.show("when the following headers match:")
        for obj in patterns:
            runner.show("  {name}: {regex_match}".format(**obj))

        # Check the deployment exists and has the sidecar
        # FIXME: implement

        # Connect to the proxy
        runner.show("Connecting to the Telepresence Proxy")
        proxy_name = "telepresence-proxy"
        remote_info = get_remote_info(runner, proxy_name, "deployment")

        old_chatty, runner.chatty = runner.chatty, False
        _, ssh = connect(runner, remote_info, False, PortMapping())
        runner.chatty = old_chatty

        # Forward local port to the proxy's API server
        api_server_port = find_free_port()
        forward_args = [
            "-L127.0.0.1:{}:127.0.0.1:8081".format(api_server_port)
        ]
        runner.launch(
            "SSH port forward (api server)", ssh.bg_command(forward_args)
        )
        url = "http://127.0.0.1:{}/intercept/{}".format(
            api_server_port, deployment
        )
        runner.write("Proxy URL is {}".format(url))

        # Start the intercept, get the remote port on the proxy
        data = json.dumps(dict(name=name, patterns=patterns))
        response = proxy_request(runner, url, data, "POST")
        try:
            remote_port = int(response)
        except ValueError:
            raise runner.fail("Unexpected response from the proxy")

        # Forward remote proxy port to the local port. This is how the
        # intercepted requests will get from the proxy to the user's code.
        forward_args = ["-R{}:127.0.0.1:{}".format(remote_port, local_port)]
        runner.launch(
            "SSH port forward (proxy to user code)",
            ssh.bg_command(forward_args)
        )

        runner.add_cleanup(
            "Delete intercept", proxy_request, runner, url, str(remote_port),
            "DELETE"
        )

        runner.show("Intercept is running. Press Ctrl-C/Ctrl-Break to quit.")
        user_process = Popen(["cat"], stdout=DEVNULL)
        wait_for_exit(runner, user_process)


def proxy_request(runner, url: str, data_str: str, method: str):
    runner.write("Proxy ({}) {} underway...".format(url, method))
    data = data_str.encode("utf-8")
    req = Request(url, method=method)
    req.add_header('Content-Type', 'application/json; charset=utf-8')
    req.add_header('Content-Length', str(len(data)))
    last_exc = ""
    for _ in runner.loop_until(15, 0.5):
        try:
            with urlopen(req, data, timeout=2.0) as fd:
                response = fd.read().decode("utf-8")
                break
        except HTTPError as exc:
            if exc.code == 404:
                raise runner.fail(
                    "Proxy does not know about the deployment. Perhaps the "
                    "sidecar is unable to reach the proxy. See (docs) for "
                    "more information."
                )
            last_exc = str(exc)
        except OSError as exc:
            last_exc = str(exc)
    else:
        raise runner.fail(
            "Failed to talk to Telepresence Proxy: {}".format(last_exc)
        )
    runner.write("Proxy ({}) {} --> [{}]".format(url, method, response))
    return response
