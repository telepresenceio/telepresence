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

def main():
    parser = ArgumentParser()
    parser.parse_args()

    result = dumps({
        "environ": dict(environ),
    })

    delimiter = "{probe delimiter}"
    print("{}{}{}".format(delimiter, result, delimiter))

main()
