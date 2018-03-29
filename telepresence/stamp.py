"""
Telepresence helper tool to time and origin stamp logfile lines.
"""

import argparse
import os
import sys
import time

import telepresence

__version__ = telepresence.__version__


def main():
    """Time and origin stamp lines, passing through from stdin to stdout"""

    parser = argparse.ArgumentParser(
        allow_abbrev=False,  # can make adding changes not backwards compatible
        description=(
            "stamp-telepresence: "
            "Time and origin stamp lines, "
            "passing through from stdin to stdout. "
            "Helper for Telepresence. "
            "Visit https://telepresence.io for more info."
        )
    )
    parser.add_argument("--version", action="version", version=__version__)
    parser.add_argument(
        "--start-time",
        type=float,
        default=time.time(),
        help=(
            "Start time for timestamps as float seconds from the Unix epoch "
            "(i.e. as returned by time.time()); default is now."
        )
    )
    parser.add_argument(
        "--id",
        default="[{}]".format(os.getpid()),
        help="Origin's identifier string"
    )
    args = parser.parse_args()

    start_time = args.start_time
    origin_id = "{:>3s} |".format(args.id)
    curtime = time.time
    out_write = sys.stdout.write
    out_flush = sys.stdout.flush
    """actual_start_time = curtime()"""

    for line in sys.stdin:
        out_write(
            "{:6.1f} {} {}".format(curtime() - start_time, origin_id, line)
        )
        out_flush()
    """
    out_write(
        "{:6.1f} STA | [{}] ran in {:0.2f} secs.\n".format(
            curtime() - start_time, args.id,
            curtime() - actual_start_time
        )
    )
    """


def run_stamp():
    """Run stamp-telepresence"""
    if sys.version_info[:2] < (3, 5):
        exit("Telepresence requires Python 3.5 or later.")
    main()


if __name__ == '__main__':
    run_stamp()
