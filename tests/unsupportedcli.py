#!/usr/bin/env python3

"""
CLI tools like ping fail nicely.
"""

import sys
from subprocess import check_call, CalledProcessError

for command in ["ping", "nslookup", "host", "traceroute", "dig"]:
    try:
        check_call([command, "arg1"])
    except CalledProcessError as e:
        if e.returncode == 55:
            continue
        raise
    else:
        raise SystemExit("Command {} succeeded?!".format(command))

sys.exit(113)
