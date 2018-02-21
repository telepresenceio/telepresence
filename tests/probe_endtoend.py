"""
This probe runs in a Telepresence created and managed execution context.
It observes things about that environment and reports about them on stdout.
The report can be inspected by the test suite to verify Telepresence has
created the execution context correctly.
"""

from os import (
    environ,
)
from json import (
    dumps,
)
from argparse import (
    ArgumentParser,
)
from urllib.request import (
    urlopen
)

def main():
    parser = argument_parser()
    args = parser.parse_args()

    result = dumps({
        "environ": dict(environ),
        "probe-urls": list(probe_urls(args.probe_url)),
    })


    delimiter = "{probe delimiter}"
    print("{}{}{}".format(delimiter, result, delimiter))


def probe_urls(urls):
    for url in urls:
        print("Retrieving {}".format(url))
        result = urlopen(url, timeout=5).read().decode("utf-8")
        print("Got result of length {} ".format(len(result)))
        yield (url, result)


def argument_parser():
    parser = ArgumentParser()
    parser.add_argument(
        "--probe-url",
        action="append",
        help="A URL to retrieve.",
    )
    return parser


main()
