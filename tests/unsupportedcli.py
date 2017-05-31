#!/usr/bin/env python3

"""
CLI tools like ping fail nicely.
"""

import sys
import os
from subprocess import check_call, CalledProcessError

commands = ["ping", "traceroute"]
if os.environ["TELEPRESENCE_METHOD"] == "inject-tcp":
    commands.extend(["nslookup", "host", "dig"])


for command in commands:
    try:
        check_call([command, "arg1"])
    except CalledProcessError as e:
        if e.returncode == 55:
            continue
        raise
    else:
        raise SystemExit("Command {} succeeded?!".format(command))

sys.exit(113)
