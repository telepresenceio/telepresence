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

    print(dumps({
        "environ": dict(environ),
    }))

main()
