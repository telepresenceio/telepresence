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
from pathlib import Path

import telepresence


def parse_args(argv=None, only_for_commands=False):

    prog = str(Path(sys.argv[0]).name)
    parser = argparse.ArgumentParser(
        allow_abbrev=False,  # can make adding changes not backwards compatible
        formatter_class=argparse.RawDescriptionHelpFormatter,
        usage="{} [options] COMMAND ...".format(prog),
        description=(
            "Telepresence: local development proxied to a remote Kubernetes "
            "cluster.\n\n"
            "Documentation: https://telepresence.io\n"
            "Real-time help: https://d6e.co/slack\n"
            "Issue tracker: https://github.com/datawire/telepresence/issues\n"
        )
    )
    parser.add_argument(
        "--version", action="version", version=telepresence.__version__
    )

    # General options

    options_group = parser.add_argument_group("options")

    options_group.add_argument(
        "--context",
        default=None,
        help=(
            "The Kubernetes context to use. Defaults to the current "
            "kubectl context."
        )
    )
    options_group.add_argument(
        "--namespace",
        default=None,
        help=(
            "The Kubernetes namespace to use. Defaults to kubectl's default "
            "for the current or specified context, "
            """which is usually "default"."""
        )
    )
    options_group.add_argument(
        "--logfile",
        default="./telepresence.log",
        help=(
            """The path to write logs to. "-" means stdout, """
            """default is "./telepresence.log"."""
        )
    )
    options_group.add_argument(
        "--verbose",
        action="store_true",
        help="Enables verbose logging for troubleshooting."
    )

    # Commands

    subparsers = parser.add_subparsers(
        title="commands",
        prog="{} [options]".format(prog),
        description="The following commands are EXPERIMENTAL as of Nov 2018",
        metavar="COMMAND",
        dest="command"
    )
    available_commands = []

    def add_command(name, *args, **kwargs):
        available_commands.append(name)
        return subparsers.add_parser(name, *args, **kwargs)

    proxy_desc = "Start or stop the Telepresence proxy pod."
    proxy_parser = add_command(
        "proxy", description=proxy_desc, help=proxy_desc
    )
    proxy_parser.add_argument(
        "start/stop",
        choices=("start", "stop"),
        help="Whether to start or stop the proxy"
    )

    outbound_desc = """
    Set up the network so that local connections can transparently reach the
    cluster. This operation will run a subprocess using "sudo", which may
    prompt you for your password.
    """
    # outbound_parser =  # commented out because we aren't using this yet
    add_command("outbound", description=outbound_desc, help=outbound_desc)

    intercept_desc = """
    Arrange for a subset of requests to be diverted to the local machine.
    Requires the Telepresence Sidecar on the deployment and the Telepresence
    Proxy running in the cluster in the same namespace.
    See (docs) for more info.
    """
    intercept_parser = add_command(
        "intercept", description=intercept_desc, help=intercept_desc
    )
    intercept_parser.add_argument(
        "deployment",
        help="The deployment to intercept",
    )
    intercept_parser.add_argument(
        "--name",
        "-n",
        help="Name of session (visible in the logs)",
    )
    intercept_parser.add_argument(
        "--port",
        "-p",
        type=int,
        required=True,
        help="Local port where your code listens"
    )
    intercept_parser.add_argument(
        "--match",
        "-m",
        nargs=2,
        action="append",
        required=True,
        help="Match the value of this header against the regular expression ",
    )

    add_command("version")

    # Perform the parsing

    show_warning_message = False
    if only_for_commands:
        # If no supported command is mentioned, do nothing
        my_argv = argv or sys.argv
        command_found = False
        for command_name in available_commands:
            if command_name in my_argv:
                command_found = True
                break
        if not command_found:
            return None
        show_warning_message = True

    try:
        args = parser.parse_args(argv)
        show_warning_message = False
    finally:
        if show_warning_message:
            msg = (
                """\nSee also "{} --help" and "{} --help-experimental" """ +
                "for more information"
            )
            print(msg.format(prog, prog))

    if args.command == "version":
        parser.parse_args(["--version"])

    return args


def show_command_help_and_quit():
    parse_args(["--help"], only_for_commands=False)


if __name__ == "__main__":
    res = parse_args(None, only_for_commands=True)
    print(res)
