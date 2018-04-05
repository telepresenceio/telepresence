import shlex
import sys
from subprocess import Popen, PIPE, DEVNULL, CalledProcessError
from threading import Thread
from time import time, ctime
from typing import List

from inspect import getframeinfo, currentframe
import os

import telepresence
from .cache import Cache


class Span(object):
    emit_summary = False

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
        spent = self.end_time - self.start_time
        if self.runner.current_span == self:
            self.runner.current_span = self.parent
        if self.verbose:
            self.runner.write("END SPAN {} {:6.1f}s".format(self.tag, spent))
        if self.parent is None and Span.emit_summary:
            self.runner.write("SPAN SUMMARY:")
            self.summarize()
        return spent

    def summarize(self):
        indent = self.depth * "  "
        if self.end_time:
            spent = "{:6.1f}s".format(self.end_time - self.start_time)
        else:
            spent = "   ???"
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
        self.write(
            "Telepresence {} launched at {}\n  {}".format(
                telepresence.__version__, ctime(), str_command(sys.argv)
            )
        )
        report = (
            ["kubectl", "version", "--short"],
            ["oc", "version"],
            ["uname", "-a"],
        )
        for command in report:
            try:
                self.popen(command)
            except OSError:
                pass
        self.write("Python {}".format(sys.version))

        cache_dir = os.path.expanduser("~/.cache/telepresence")
        os.makedirs(cache_dir, exist_ok=True)
        self.cache = Cache.load(os.path.join(cache_dir, "cache.json"))
        self.cache.invalidate(12 * 60 * 60)

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
            try:
                open(logfile_path, "w").close()
            except OSError as exc:
                exit(
                    "Failed to open logfile ({}): {}".format(
                        logfile_path, exc
                    )
                )

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

    def write(self, message: str, prefix="TEL") -> None:
        """Write a message to the log."""
        for sub_message in message.splitlines():
            line = "{:6.1f} {} | {}\n".format(
                time() - self.start_time, prefix, sub_message.rstrip()
            )
            self.logfile.write(line)
        self.logfile.flush()

    def set_success(self, flag: bool) -> None:
        """Indicate whether the command succeeded"""
        Span.emit_summary = flag
        self.write("Success. Starting cleanup.")

    def command_span(self, track, args):
        return self.span(
            "{} {}".format(track, str_command(args))[:80],
            False,
            verbose=False
        )

    def make_logger(self, track, capture=None):
        """Create a logger that optionally captures what is logged"""
        prefix = "{:>3d}".format(track)

        if capture is None:

            def logger(line):
                """Just log"""
                self.write(line, prefix=prefix)
        else:

            def logger(line):
                """Log and capture"""
                capture.append(line)
                self.write(line, prefix=prefix)

        return logger

    def launch_command(self, track, out_cb, err_cb, args, **kwargs) -> Popen:
        """Call a command, generate stamped, logged output."""
        try:
            process = launch_command(args, out_cb, err_cb, **kwargs)
        except OSError as exc:
            self.write("[{}] {}".format(track, exc))
            raise
        return process

    def run_command(self, track, msg1, msg2, out_cb, err_cb, args, **kwargs):
        """Run a command synchronously"""
        self.write("[{}] {}: {}".format(track, msg1, str_command(args)))
        span = self.command_span(track, args)
        process = self.launch_command(track, out_cb, err_cb, args, **kwargs)
        process.wait()
        spent = span.end()
        retcode = process.poll()
        if retcode:
            self.write(
                "[{}] exit {} in {:0.2f} secs.".format(track, retcode, spent)
            )
            raise CalledProcessError(retcode, args)
        if spent > 1:
            self.write("[{}] {} in {:0.2f} secs.".format(track, msg2, spent))

    def check_call(self, args, **kwargs):
        """Run a subprocess, make sure it exited with 0."""
        self.counter = track = self.counter + 1
        out_cb = err_cb = self.make_logger(track)
        self.run_command(
            track, "Running", "ran", out_cb, err_cb, args, **kwargs
        )

    def get_output(self, args, reveal=False, **kwargs) -> str:
        """Return (stripped) command result as unicode string."""
        self.counter = track = self.counter + 1
        capture = []  # type: List[str]
        if reveal or self.verbose:
            out_cb = self.make_logger(track, capture=capture)
        else:
            out_cb = capture.append
        err_cb = self.make_logger(track)
        self.run_command(
            track, "Capturing", "captured", out_cb, err_cb, args, **kwargs
        )
        return "".join(capture).strip()

    def popen(self, args, **kwargs) -> Popen:
        """Return Popen object."""
        self.counter = track = self.counter + 1
        out_cb = err_cb = self.make_logger(track)
        done = lambda process: self._popen_done(track, process)
        self.write("[{}] Launching: {}".format(track, str_command(args)))
        process = self.launch_command(
            track, out_cb, err_cb, args, done=done, **kwargs
        )
        return process

    def _popen_done(self, track, process):
        retcode = process.poll()
        if retcode is not None:
            self.write("[{}] exit {}".format(track, retcode))

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


def str_command(args: List[str]):
    """
    Return a string representing the shell command and its arguments.

    :param args: Shell command and its arguments
    :return: String representation thereof
    """
    res = []
    for arg in args:
        if "\n" in arg:
            res.append(repr(arg))
        else:
            res.append(shlex.quote(arg))
    return " ".join(res)


def launch_command(args, out_cb, err_cb, done=None, **kwargs):
    """
    Launch subprocess with args, kwargs.
    Log stdout and stderr by calling respective callbacks.
    """

    def pump_stream(callback, stream):
        """Pump the stream"""
        for line in stream:
            callback(line)

    def joiner():
        """Wait for streams to finish, then call done callback"""
        for th in threads:
            th.join()
        done(process)

    kwargs = kwargs.copy()
    in_data = kwargs.get("input")
    if "input" in kwargs:
        del kwargs["input"]
        assert kwargs.get("stdin") is None, kwargs["stdin"]
        kwargs["stdin"] = PIPE
    elif "stdin" not in kwargs:
        kwargs["stdin"] = DEVNULL
    kwargs.setdefault("stdout", PIPE)
    kwargs.setdefault("stderr", PIPE)
    kwargs["universal_newlines"] = True  # Text streams, not byte streams
    process = Popen(args, **kwargs)
    threads = []
    if process.stdout:
        thread = Thread(
            target=pump_stream, args=(out_cb, process.stdout), daemon=True
        )
        thread.start()
        threads.append(thread)
    if process.stderr:
        thread = Thread(
            target=pump_stream, args=(err_cb, process.stderr), daemon=True
        )
        thread.start()
        threads.append(thread)
    if done and threads:
        Thread(target=joiner, daemon=True).start()
    if in_data:
        process.stdin.write(in_data)
        process.stdin.close()
    return process
