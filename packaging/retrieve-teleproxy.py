#!/usr/bin/env python3
"""
Retrieve the right version of teleproxy
"""

from pathlib import Path
from urllib.request import urlopen
import sys

TELEPROXY_BASE = "https://s3.amazonaws.com/datawire-static-files/teleproxy/"
TELEPROXY_VERSION = "0.3.6"


def retrieve_teleproxy(version, go_os, go_arch, output):
    """
    Fetch externally-built teleproxy binary
    """
    url = TELEPROXY_BASE + "{}/{}/{}/teleproxy".format(version, go_os, go_arch)
    try:
        with urlopen(url) as down, output.open("wb") as out:
            while True:
                data = down.read(2**18)
                if not data:
                    break
                out.write(data)
        output.chmod(0o755)
    except Exception:
        print(url)
        print(output)
        raise
    print("Downloaded {}".format(output))


def main():
    """
    Set things up then call the code that retrieves binaries
    """
    # See  `go tool dist list` for Go OS and arch options
    go_arch = "amd64"

    # Make a list of required downloads
    downloads = []
    if len(sys.argv) > 1:
        # Install script usage; just download the required version
        go_os = sys.platform
        downloads.append((go_os, go_arch, Path(sys.argv[1])))
    else:
        # Deploy script usage; download everything
        dist = Path(__file__).absolute().resolve().parent.parent / "dist"
        dist.mkdir(parents=True, exist_ok=True)
        ptn = "teleproxy-{}-{}"
        for go_os in "darwin", "linux":
            filename = ptn.format(go_os, go_arch)
            downloads.append((go_os, go_arch, dist / filename))

    # Download
    for go_os, go_arch, target in downloads:
        retrieve_teleproxy(TELEPROXY_VERSION, go_os, go_arch, target)


if __name__ == "__main__":
    main()
