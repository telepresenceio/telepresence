import sys
from subprocess import Popen, PIPE, STDOUT, DEVNULL, CalledProcessError, \
    check_output
from time import time, ctime
from typing import List

from inspect import getframeinfo, currentframe
import os
from .cache import Cache


class Span(object):
    def __init__(self, runner, tag, parent, verbose=True):
        self.runner = runner
        self.tag = tag
        self.parent = parent
        self.children = []
        if self.parent:
            self.parent.children.append(self)
            self.depth = self.parent.depth + 1
        else:
            self.depth = 0
        self.start_time = None
        self.end_time = None
        self.verbose = verbose

    def begin(self):
        self.start_time = time()
        if self.verbose:
            self.runner.write("BEGIN SPAN {}".format(self.tag))

    def end(self):
        self.end_time = time()
        if self.runner.current_span == self:
            self.runner.current_span = self.parent
        if self.verbose:
            spent = self.end_time - self.start_time
            self.runner.write("END SPAN {} {:6.1f}s".format(self.tag, spent))
        if self.parent is None:
            self.runner.write("SPAN SUMMARY:")
            self.summarize()

    def summarize(self):
        indent = self.depth * "  "
        if self.end_time:
            spent = "{:6.1f}s".format(self.end_time - self.start_time)
        else:
            spent = "???"
        self.runner.write("{}{} {}".format(spent, indent, self.tag))
        for ch in self.children:
            ch.summarize()


class Runner(object):
    """Context for running subprocesses."""

    def __init__(self, logfile, kubectl_cmd: str, verbose: bool) -> None:
        """
        :param logfile: file-like object to write logs to.
        :param kubectl_cmd: Command to run for kubectl, either "kubectl" or
            "oc" (for OpenShift Origin).
        :param verbose: Whether subcommand should run in verbose mode.
        """
        self.logfile = logfile
        self.kubectl_cmd = kubectl_cmd
        self.verbose = verbose
        self.start_time = time()
        self.current_span = None  # type: Span
        self.counter = 0
        self.write("Telepresence launched at {}".format(ctime()))
        self.write("  {}".format(sys.argv))
        cache_dir = os.path.expanduser("~/.cache/telepresence")
        os.makedirs(cache_dir, exist_ok=True)
        self.cache = Cache.load(os.path.join(cache_dir, "cache.json"))

    @classmethod
    def open(cls, logfile_path, kubectl_cmd: str, verbose: bool):
        """
        :return: File-like object for the given logfile path.
        """
        if logfile_path == "-":
            return cls(sys.stdout, kubectl_cmd, verbose)
        else:
            # Wipe existing logfile, open using append mode so multiple
            # processes don't clobber each other's outputs, and use line
            # buffering so data gets written out immediately.
            if os.path.exists(logfile_path):
                open(logfile_path, "w").close()
            return cls(
                open(logfile_path, "a", buffering=1), kubectl_cmd, verbose
            )

    def span(self, name: str = "", context=True, verbose=True) -> Span:
        """Write caller's frame info to the log."""

        if context:
            info = getframeinfo(currentframe().f_back)
            tag = "{}:{}({})".format(
                os.path.basename(info.filename), info.lineno,
                "{},{}".format(info.function, name) if name else info.function
            )
        else:
            tag = name
        s = Span(self, tag, self.current_span, verbose=verbose)
        self.current_span = s
        s.begin()
        return s

    def write(self, message: str) -> None:
        """Write a message to the log."""
        message = message.rstrip()
        line = "{:6.1f} TL | {}\n".format(time() - self.start_time, message)
        self.logfile.write(line)
        self.logfile.flush()

    def command_span(self, track, args):
        return self.span(
            "{} {}".format(track, " ".join(args))[:80], False, verbose=False
        )

    def launch_command(self, track, args, **kwargs) -> Popen:
        """Call a command, generate stamped, logged output."""
        kwargs = kwargs.copy()
        in_data = kwargs.get("input")
        if "input" in kwargs:
            del kwargs["input"]
            kwargs["stdin"] = PIPE
        kwargs["stdout"] = PIPE
        kwargs["stderr"] = STDOUT
        process = Popen(args, **kwargs)
        Popen([
            "stamp-telepresence", "--id", "{} |".format(track), "--start-time",
            str(self.start_time)
        ],
              stdin=process.stdout,
              stdout=self.logfile,
              stderr=self.logfile)
        if in_data:
            process.communicate(in_data, timeout=kwargs.get("timeout"))
        return process

    def check_call(self, args, **kwargs):
        """Run a subprocess, make sure it exited with 0."""
        self.counter = track = self.counter + 1
        self.write("[{}] Running: {}... ".format(track, args))
        if "input" not in kwargs and "stdin" not in kwargs:
            kwargs["stdin"] = DEVNULL
        span = self.command_span(track, args)
        try:
            process = self.launch_command(track, args, **kwargs)
            process.wait()
        finally:
            span.end()
        retcode = process.poll()
        if retcode:
            self.write("[{}] exit {}.".format(track, retcode))
            raise CalledProcessError(retcode, args)
        self.write("[{}] ran.".format(track))

    def get_output(self, args, stderr=None, **kwargs) -> str:
        """Return (stripped) command result as unicode string."""
        if stderr is None:
            stderr = self.logfile
        self.counter = track = self.counter + 1
        self.write("[{}] Capturing: {}...".format(track, args))
        kwargs["stdin"] = DEVNULL
        kwargs["stderr"] = stderr
        span = self.command_span(track, args)
        try:
            result = str(check_output(args, **kwargs).strip(), "utf-8")
        finally:
            span.end()
        self.write("[{}] captured.".format(track))
        return result

    def popen(self, args, stdin=DEVNULL, **kwargs) -> Popen:
        """Return Popen object."""
        self.counter = track = self.counter + 1
        self.write("[{}] Launching: {}...".format(track, args))
        kwargs["stdin"] = stdin
        result = self.launch_command(track, args, **kwargs)
        return result

    def kubectl(self, context: str, namespace: str,
                args: List[str]) -> List[str]:
        """Return command-line for running kubectl."""
        result = [self.kubectl_cmd]
        if self.verbose:
            result.append("--v=4")
        result.extend(["--context", context])
        result.extend(["--namespace", namespace])
        result += args
        return result

    def get_kubectl(
        self, context: str, namespace: str, args: List[str], stderr=None
    ) -> str:
        """Return output of running kubectl."""
        return self.get_output(
            self.kubectl(context, namespace, args), stderr=stderr
        )

    def check_kubectl(
        self, context: str, namespace: str, kubectl_args: List[str], **kwargs
    ) -> None:
        """Check exit code of running kubectl."""
        self.check_call(
            self.kubectl(context, namespace, kubectl_args), **kwargs
        )


def read_logs(logfile) -> str:
    """Read logfile, return string."""
    logs = "Not available"
    if logfile != "-" and os.path.exists(logfile):
        try:
            with open(logfile, "r") as logfile:
                logs = logfile.read()
        except Exception as e:
            logs += ", error ({})".format(e)
    return logs
