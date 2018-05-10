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

import argparse
import sys
import webbrowser
from traceback import print_exc
from urllib.parse import quote_plus

from io import StringIO
from subprocess import check_output
from typing import List, Set, Tuple, Union
from pathlib import Path
from functools import wraps

import telepresence
from telepresence.utilities import random_name


class PortMapping(object):
    """Maps local ports to listen to remote exposed ports."""

    def __init__(self):
        self._mapping = {}  # type: Dict[int,int]

    @classmethod
    def parse(cls, port_strings: List[str]):
        """Parse list of 'port' or 'local_port:remote_port' to PortMapping."""
        result = PortMapping()
        for port_string in port_strings:
            if ":" in port_string:
                local_port, remote_port = map(int, port_string.split(":"))
            else:
                local_port, remote_port = int(port_string), int(port_string)
            result._mapping[local_port] = remote_port
        return result

    def merge_automatic_ports(self, ports: List[int]):
        """
        Merge a list of ports to the existing ones.

        The existing ones will win if the port is already there.
        """
        remote = self.remote()
        for port in ports:
            if port in remote:
                continue
            self._mapping[port] = port

    def remote(self) -> Set[int]:
        """Return set of remote ports."""
        return set(self._mapping.values())

    def local_to_remote(self) -> Set[Tuple[int, int]]:
        """Return set of pairs of local, remote ports."""
        return set(self._mapping.items())


class handle_unexpected_errors(object):
    """Decorator that catches unexpected errors."""

    def __init__(self, session):
        self.session = session

    def __call__(self, f):
        def safe_output(args):
            try:
                return str(check_output(args),
                           "utf-8").strip().replace("\n", " // ")
            except Exception as e:
                return "(error: {})".format(e)

        @wraps(f)
        def call_f(*args, **kwargs):
            try:
                return f(*args, **kwargs)
            except SystemExit:
                raise
            except KeyboardInterrupt:
                raise SystemExit(0)
            except Exception as e:
                try:
                    logs = self.session.output.read_logs()
                    log_path = self.session.output.logfile_path
                except AttributeError:
                    # No session or no output
                    logs = "Not available"
                    log_path = "-"
                errorf = StringIO()
                print_exc(file=errorf)
                error = errorf.getvalue()
                print(
                    "\nLooks like there's a bug in our code. Sorry about that!"
                    "\n\n"
                    "Here's the traceback:\n\n" + error + "\n"
                )
                if log_path != "-":
                    log_ref = " (see {} for the complete logs):".format(
                        log_path
                    )
                else:
                    log_ref = ""
                if "\n" in logs:
                    print(
                        "And here are the last few lines of the logfile" +
                        log_ref + "\n\n" + "\n".join(logs.splitlines()[-20:]) +
                        "\n"
                    )

                if sys.stdout.isatty() and input(
                    "Would you like to file an issue in our issue tracker?"
                    " You'll be able to review and edit before anything is"
                    " posted to the public."
                    " We'd really appreciate the help improving our "
                    "product. [Y/n]: ",
                ).lower() in ("y", ""):
                    url = (
                        "https://github.com/datawire/telepresence/issues/" +
                        "new?body="
                    )
                    body = quote_plus(
                        # Overly long URLs won't work:
                        BUG_REPORT_TEMPLATE.format(
                            sys.argv, telepresence.__version__, sys.version,
                            safe_output(["kubectl", "version", "--short"]),
                            safe_output(["oc", "version"]),
                            safe_output(["uname", "-a"]), error, logs[-1000:]
                        )[:4000]
                    )
                    webbrowser.open_new(url + body)
                else:
                    raise SystemExit(1)

        return call_f


def path_or_bool(value: str) -> Union[Path, bool]:
    """Parse value as a Path or a boolean"""
    path = Path(value)
    if path.is_absolute():
        return path
    value = value.lower()
    if value in ("true", "on", "yes", "1"):
        return True
    if value in ("false", "off", "no", "0"):
        return False
    raise argparse.ArgumentTypeError(
        "Value must be true, false, or an absolute filesystem path"
    )


@handle_unexpected_errors("-")
def parse_args(args=None) -> argparse.Namespace:
    """Create a new ArgumentParser and parse sys.argv."""
    parser = argparse.ArgumentParser(
        formatter_class=argparse.RawDescriptionHelpFormatter,
        allow_abbrev=False,  # can make adding changes not backwards compatible
        description=(
            "Telepresence: local development proxied to a remote Kubernetes "
            "cluster.\n\n"
            "Documentation: https://telepresence.io\n"
            "Real-time help: https://gitter.im/datawire/telepresence\n"
            "Issue tracker: https://github.com/datawire/telepresence/issues\n"
            "\n" + HELP_EXAMPLES + "\n\n"
        )
    )
    parser.add_argument(
        '--version', action='version', version=telepresence.__version__
    )
    parser.add_argument(
        "--verbose",
        action='store_true',
        help="Enables verbose logging for troubleshooting."
    )
    parser.add_argument(
        "--logfile",
        default="./telepresence.log",
        help=(
            "The path to write logs to. '-' means stdout, "
            "default is './telepresence.log'."
        )
    )
    parser.add_argument(
        "--method",
        "-m",
        choices=["inject-tcp", "vpn-tcp", "container"],
        help=(
            "'inject-tcp': inject process-specific shared "
            "library that proxies TCP to the remote cluster.\n"
            "'vpn-tcp': all local processes can route TCP "
            "traffic to the remote cluster. Requires root.\n"
            "'container': used with --docker-run.\n"
            "\n"
            "Default is 'vpn-tcp', or 'container' when --docker-run is used.\n"
            "\nFor more details see "
            "https://telepresence.io/reference/methods.html"
        )
    )
    group_deployment = parser.add_mutually_exclusive_group()
    group_deployment.add_argument(
        '--new-deployment',
        "-n",
        metavar="DEPLOYMENT_NAME",
        dest="new_deployment",
        help=(
            "Create a new Deployment in Kubernetes where the "
            "datawire/telepresence-k8s image will run. It will be deleted "
            "on exit. If no deployment option is specified this will be "
            " used by default, with a randomly generated name."
        )
    )
    group_deployment.add_argument(
        "--swap-deployment",
        "-s",
        dest="swap_deployment",
        metavar="DEPLOYMENT_NAME[:CONTAINER]",
        help=(
            "Swap out an existing deployment with the Telepresence proxy, "
            "swap back on exit. If there are multiple containers in the pod "
            "then add the optional container name to indicate which container"
            " to use."
        )
    )
    group_deployment.add_argument(
        "--deployment",
        "-d",
        metavar="EXISTING_DEPLOYMENT_NAME",
        help=(
            "The name of an existing Kubernetes Deployment where the " +
            "datawire/telepresence-k8s image is already running."
        )
    )
    parser.add_argument(
        "--context",
        default=None,
        help=(
            "The Kubernetes context to use. Defaults to current kubectl"
            " context."
        )
    )
    parser.add_argument(
        "--namespace",
        default=None,
        help=(
            "The Kubernetes namespace to use. Defaults to kubectl's default"
            " for the current context, which is usually 'default'."
        )
    )
    parser.add_argument(
        "--expose",
        action='append',
        metavar="PORT[:REMOTE_PORT]",
        default=[],
        help=(
            "Port number that will be exposed to Kubernetes in the Deployment."
            " Should match port exposed in the existing Deployment if using "
            "--deployment or --swap-deployment. By default local port and "
            "remote port are the same; if you want to listen on port 8080 "
            "locally but be exposed as port 80 in Kubernetes you can do "
            "'--expose 8080:80'."
        )
    )
    parser.add_argument(
        "--also-proxy",
        metavar="CLOUD_HOSTNAME",
        dest="also_proxy",
        action='append',
        default=[],
        help=(
            "If you are using --method=vpn-tcp, use this to add additional "
            "remote IPs, IP ranges, or hostnames to proxy. Kubernetes service "
            "and pods are proxied automatically, so you only need to list "
            "cloud resources, e.g. the hostname of a AWS RDS. "
            "When using --method=inject-tcp "
            "this option is unnecessary as all outgoing communication in "
            "the run subprocess will be proxied."
        )
    )
    parser.add_argument(
        "--mount",
        type=path_or_bool,
        metavar="PATH_OR_BOOLEAN",
        dest="mount",
        default=True,
        help=(
            "The absolute path for the root directory where volumes will be "
            "mounted, $TELEPRESENCE_ROOT. "
            "Use \"true\" to have Telepresence pick a random mount point "
            "under /tmp (default). "
            "Use \"false\" to disable filesystem mounting entirely."
        )
    )

    group = parser.add_mutually_exclusive_group()
    group.add_argument(
        "--run-shell",
        dest="runshell",
        action="store_true",
        help="Run a local shell that will be proxied to/from Kubernetes.",
    )
    group.add_argument(
        "--run",
        metavar=("COMMAND", "ARG"),
        dest="run",
        nargs=argparse.REMAINDER,
        help=(
            "Run the specified command arguments, e.g. "
            "'--run python myapp.py'."
        )
    )
    group.add_argument(
        "--docker-run",
        metavar="DOCKER_RUN_ARG",
        dest="docker_run",
        nargs=argparse.REMAINDER,
        help=(
            "Run a Docker container, by passing the arguments to 'docker run',"
            " e.g. '--docker-run -i -t ubuntu:16.04 /bin/bash'. "
            "Requires --method container."
        )
    )
    args = parser.parse_args(args)

    # Fill in defaults:
    if args.method is None:
        if args.docker_run is not None:
            args.method = "container"
        else:
            args.method = "vpn-tcp"
    if args.deployment is None and args.new_deployment is None and (
        args.swap_deployment is None
    ):
        args.new_deployment = random_name()

    if args.method == "container" and args.docker_run is None:
        raise SystemExit(
            "'--docker-run' is required when using '--method container'."
        )
    if args.docker_run is not None and args.method != "container":
        raise SystemExit(
            "'--method container' is required when using '--docker-run'."
        )

    args.expose = PortMapping.parse(args.expose)
    return args


HELP_EXAMPLES = """\
== Examples ==

Send a HTTP query to Kubernetes Service called 'myservice' listening on port \
8080:

$ telepresence --run curl http://myservice:8080/

Replace an existing Deployment 'myserver' listening on port 9090 with a local \
process listening on port 9090:

$ telepresence --swap-deployment myserver --expose 9090 \
  --run python3 -m http.server 9090

Use a different local port than the remote port:

$ telepresence --swap-deployment myserver --expose 9090:80 \
  --run python3 -m http.server 9090

Run a Docker container instead of a local process:

$ telepresence --swap-deployment myserver --expose 80 \
  --docker-run -i -t nginx:latest


== Detailed usage ==
"""

BUG_REPORT_TEMPLATE = u"""\
### What were you trying to do?

(please tell us)

### What did you expect to happen?

(please tell us)

### What happened instead?

(please tell us - the traceback is automatically included, see below)

### Automatically included information

Command line: `{}`
Version: `{}`
Python version: `{}`
kubectl version: `{}`
oc version: `{}`
OS: `{}`
Traceback:

```
{}
```

Logs:

```
{}
```
"""
