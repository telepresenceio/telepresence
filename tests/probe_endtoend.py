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
