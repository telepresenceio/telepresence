# Copyright 2018 Datawire. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
import os
import signal
import sys
import textwrap
import typing
from contextlib import contextmanager
from inspect import currentframe, getframeinfo
from subprocess import CalledProcessError, DEVNULL, PIPE, Popen, check_output
from threading import Thread
from time import sleep, time

from telepresence.cache import Cache
from telepresence.output import Output
from telepresence.span import Span
from telepresence.utilities import str_command

_CleanupItem = typing.NamedTuple(
    "_CleanupItem", [
        ("name", str),
        ("callable", typing.Callable),
        ("args", typing.Tuple),
        ("kwargs", typing.Dict[str, typing.Any]),
    ]
)


class Runner(object):
    """Context for running subprocesses."""

    def __init__(
        self, output: Output, kubectl_cmd: str, verbose: bool
    ) -> None:
        """
        :param output: The Output instance for the session
        :param kubectl_cmd: Command to run for kubectl, either "kubectl" or
            "oc" (for OpenShift Origin).
        :param verbose: Whether subcommand should run in verbose mode.
        """
        self.output = output
        self.kubectl_cmd = kubectl_cmd
        self.verbose = verbose
        self.start_time = time()
        self.current_span = None  # type: typing.Optional[Span]
        self.counter = 0
        self.cleanup_stack = []  # type: typing.List[_CleanupItem]

        if sys.stderr.isatty():
            try:
                term_width = int(check_output(["tput", "cols"]))
            except (CalledProcessError, OSError):
                term_width = 79
        else:
            term_width = 99999
        self.wrapper = textwrap.TextWrapper(
            width=term_width,
            initial_indent="T: ",
            subsequent_indent="T: ",
            replace_whitespace=False,
            drop_whitespace=False,
        )

        # Log some version info
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
        self.output.write("Python {}".format(sys.version))

        cache_dir = os.path.expanduser("~/.cache/telepresence")
        os.makedirs(cache_dir, exist_ok=True)
        self.cache = Cache.load(os.path.join(cache_dir, "cache.json"))
        self.cache.invalidate(12 * 60 * 60)
        self.add_cleanup("Save caches", self.cache.save)

    @classmethod
    def open(cls, logfile_path, kubectl_cmd: str, verbose: bool):
        """
        :return: File-like object for the given logfile path.
        """
        output = Output(logfile_path)
        return cls(output, kubectl_cmd, verbose)

    def span(self, name: str = "", context=True, verbose=True) -> Span:
        """Write caller's frame info to the log."""

        if context:
            frame = currentframe()
            assert frame is not None  # mypy
            info = getframeinfo(frame.f_back)
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
        """Don't use this..."""
        return self.output.write(message, prefix)

    def read_logs(self) -> str:
        """Return the end of the contents of the log"""
        sleep(2.0)
        return self.output.read_logs()

    def set_success(self, flag: bool) -> None:
        """Indicate whether the command succeeded"""
        Span.emit_summary = flag
        self.output.write("Success. Starting cleanup.")

    def show(self, message: str) -> None:
        """Display a message to the user on stderr"""
        self.write(message, prefix=">>>")
        for line in message.splitlines():
            print(self.wrapper.fill(line), file=sys.stderr)

    # Subprocesses

    def _make_logger(self, track, capture=None):
        """Create a logger that optionally captures what is logged"""
        prefix = "{:>3d}".format(track)

        if capture is None:

            def logger(line):
                """Just log"""
                if line is not None:
                    self.output.write(line, prefix=prefix)
        else:

            def logger(line):
                """Log and capture"""
                capture.append(line)
                if line is not None:
                    self.output.write(line, prefix=prefix)

        return logger

    def _launch_command(self, track, out_cb, err_cb, args, **kwargs) -> Popen:
        """Call a command, generate stamped, logged output."""
        try:
            process = _launch_command(args, out_cb, err_cb, **kwargs)
        except OSError as exc:
            self.output.write("[{}] {}".format(track, exc))
            raise
        # Grep-able log: self.output.write("CMD: {}".format(str_command(args)))
        return process

    def _run_command(self, track, msg1, msg2, out_cb, err_cb, args, **kwargs):
        """Run a command synchronously"""
        self.output.write("[{}] {}: {}".format(track, msg1, str_command(args)))
        span = self.span(
            "{} {}".format(track, str_command(args))[:80],
            False,
            verbose=False
        )
        process = self._launch_command(track, out_cb, err_cb, args, **kwargs)
        process.wait()
        spent = span.end()
        retcode = process.poll()
        if retcode:
            self.output.write(
                "[{}] exit {} in {:0.2f} secs.".format(track, retcode, spent)
            )
            raise CalledProcessError(retcode, args)
        if spent > 1:
            self.output.write(
                "[{}] {} in {:0.2f} secs.".format(track, msg2, spent)
            )

    def check_call(self, args, **kwargs):
        """Run a subprocess, make sure it exited with 0."""
        self.counter = track = self.counter + 1
        out_cb = err_cb = self._make_logger(track)
        self._run_command(
            track, "Running", "ran", out_cb, err_cb, args, **kwargs
        )

    def get_output(self, args, reveal=False, **kwargs) -> str:
        """Return (stripped) command result as unicode string."""
        self.counter = track = self.counter + 1
        capture = []  # type: typing.List[str]
        if reveal or self.verbose:
            out_cb = self._make_logger(track, capture=capture)
        else:
            out_cb = capture.append
        err_cb = self._make_logger(track)
        cpe_exc = None
        try:
            self._run_command(
                track, "Capturing", "captured", out_cb, err_cb, args, **kwargs
            )
        except CalledProcessError as exc:
            cpe_exc = exc
        # Wait for end of stream to be recorded
        while not capture or capture[-1] is not None:
            sleep(0.1)
        del capture[-1]
        output = "".join(capture).strip()
        if cpe_exc:
            raise CalledProcessError(cpe_exc.returncode, cpe_exc.cmd, output)
        return output

    def popen(self, args, **kwargs) -> Popen:
        """Return Popen object."""
        self.counter = track = self.counter + 1
        out_cb = err_cb = self._make_logger(track)

        def done(proc):
            self._popen_done(track, proc)

        self.output.write(
            "[{}] Launching: {}".format(track, str_command(args))
        )
        process = self._launch_command(
            track, out_cb, err_cb, args, done=done, **kwargs
        )
        return process

    def _popen_done(self, track, process):
        retcode = process.poll()
        if retcode is not None:
            self.output.write("[{}] exit {}".format(track, retcode))

    # kubectl

    def kubectl(self, context: str, namespace: str,
                args: typing.List[str]) -> typing.List[str]:
        """Return command-line for running kubectl."""
        result = [self.kubectl_cmd]
        if self.verbose:
            result.append("--v=4")
        result.extend(["--context", context])
        result.extend(["--namespace", namespace])
        result += args
        return result

    def get_kubectl(
        self,
        context: str,
        namespace: str,
        args: typing.List[str],
        stderr=None
    ) -> str:
        """Return output of running kubectl."""
        return self.get_output(
            self.kubectl(context, namespace, args), stderr=stderr
        )

    def check_kubectl(
        self, context: str, namespace: str, kubectl_args: typing.List[str],
        **kwargs
    ) -> None:
        """Check exit code of running kubectl."""
        self.check_call(
            self.kubectl(context, namespace, kubectl_args), **kwargs
        )

    # Cleanup

    def add_cleanup(self, name: str, callback, *args, **kwargs) -> None:
        """
        Set up callback to be called during cleanup processing on exit.

        :param name: Logged for debugging
        :param callback: What to call during cleanup
        """
        cleanup_item = _CleanupItem(name, callback, args, kwargs)
        self.cleanup_stack.append(cleanup_item)

    def _signal_received(self, sig_num, frame):
        try:
            sig_name = signal.Signals(sig_num).name
        except (ValueError, AttributeError):
            sig_name = str(sig_num)
        try:
            frame_name = frame.f_code.co_name
        except AttributeError:
            frame_name = "(unknown)"
        self.show(
            "Received signal {} while in function {}".format(
                sig_name, frame_name
            )
        )
        self.exit()

    def _do_cleanup(self):
        failures = []
        self.show("Exit cleanup in progress")
        for name, callback, args, kwargs in reversed(self.cleanup_stack):
            self.write("(Cleanup) {}".format(name))
            try:
                callback(*args, **kwargs)
            except BaseException as exc:
                self.write("(Cleanup) {} failed:".format(name))
                self.write("(Cleanup)   {}".format(exc))
                failures.append((name, exc))
        return failures

    @contextmanager
    def cleanup_handling(self):
        signal.signal(signal.SIGTERM, self._signal_received)
        signal.signal(signal.SIGHUP, self._signal_received)
        try:
            yield
        finally:
            failures = self._do_cleanup()
        if failures:
            self.show("WARNING: Failures during cleanup. See above.")

    # Exit

    def fail(self, message: str, code=1) -> SystemExit:
        """
        Report failure to the user and exit. Does not return. Cleanup will run
        before the process ends. This does not invoke the crash reporter; an
        uncaught exception will achieve that, e.g., RuntimeError.

        :param message: So the user knows what happened
        :param code: Process exit code
        """
        self.show(message)
        self.write("EXITING with status code {}".format(code))
        exit(code)
        return SystemExit(code)  # Not reached; just here for the linters

    def exit(self) -> SystemExit:
        """
        Exit after a successful session. Does not return. Cleanup will run
        before the process ends.
        """
        self.write("EXITING successful session.")
        exit(0)
        return SystemExit(0)  # Not reached; just here for the linters


def _launch_command(args, out_cb, err_cb, done=None, **kwargs):
    """
    Launch subprocess with args, kwargs.
    Log stdout and stderr by calling respective callbacks.
    """

    def pump_stream(callback, stream):
        """Pump the stream"""
        for line in stream:
            callback(line)
        callback(None)

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
        process.stdin.write(str(in_data, "utf-8"))
        process.stdin.close()
    return process
