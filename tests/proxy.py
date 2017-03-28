from subprocess import check_call
import os
import sys
from urllib.request import urlopen
from urllib.error import HTTPError


def main():
    # Add alias analiaswedefine that points at ipify Service:
    check_call([
        "kubectl", "exec",
        "--container=" + os.environ["TELEPRESENCE_CONTAINER"],
        os.environ["TELEPRESENCE_POD"], "--", "/bin/sh", "-c",
        r'''echo "\n$(host -t A {} | sed 's/.* \([.0-9]*\)/\1/)''' +
        r'''analiaswedefine\n" >> /etc/hosts'''.format(sys.argv[1]),
    ])

    try:
        urlopen("http://analiaswedefine:3000/", timeout=5).read()
    except HTTPError:
        raise SystemExit(3)


if __name__ == '__main__':
    main()
