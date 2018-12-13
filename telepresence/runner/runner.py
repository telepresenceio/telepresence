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
import uuid
from collections import deque
from contextlib import contextmanager
from functools import partial
from inspect import currentframe, getframeinfo
from pathlib import Path
from shutil import rmtree, which
from subprocess import STDOUT, CalledProcessError, Popen
from tempfile import mkdtemp
from threading import Thread
from time import sleep, time

from telepresence import TELEPRESENCE_BINARY
from telepresence.utilities import kill_process, str_command

from .cache import Cache
from .launch import BackgroundProcessCrash, _launch_command
from .output import Output
from .span import Span

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

    def __init__(self, logfile_path: str, kubeinfo, verbose: bool) -> None:
        """
        :param logfile_path: Path or string file path or "-" for stdout
        :param kubeinfo: How to run kubectl or equivalent
        :param verbose: Whether subcommand should run in verbose mode.
        """
        self.output = Output(logfile_path)
        self.logfile_path = self.output.logfile_path
        self.kubectl = kubeinfo
        self.verbose = verbose
        self.start_time = time()
        self.current_span = None  # type: typing.Optional[Span]
        self.counter = 0
        self.cleanup_stack = []  # type: typing.List[_CleanupItem]
        self.sudo_held = False
        self.quitting = False
        self.ended = []  # type: typing.List[str]

        if sys.platform.startswith("linux"):
            self.platform = "linux"
        elif sys.platform.startswith("darwin"):
            self.platform = "darwin"
        else:
            # For untested platforms...
            self.platform = sys.platform
        self.output.write("Platform: {}".format(self.platform))

        term_width = 99999
        self.chatty = False
        if sys.stderr.isatty():
            err_fd = sys.stderr.fileno()
            try:
                term_width = os.get_terminal_size(err_fd).columns - 1
                self.chatty = True
            except OSError:
                pass
        self.wrapper = textwrap.TextWrapper(
            width=term_width,
            initial_indent="T: ",
            subsequent_indent="T: ",
            replace_whitespace=False,
            drop_whitespace=False,
        )
        self.raw_wrapper = textwrap.TextWrapper(
            width=99999,
            initial_indent="T: ",
            subsequent_indent="T: ",
            replace_whitespace=False,
            drop_whitespace=False,
        )
        self.session_id = uuid.uuid4().hex

        # Log some version info
        self.output.write("Python {}".format(sys.version))
        self.check_call(["uname", "-a"])

        cache_dir = os.path.expanduser("~/.cache/telepresence")
        os.makedirs(cache_dir, exist_ok=True)
        self.cache = Cache.load(os.path.join(cache_dir, "cache.json"))
        self.cache.invalidate(12 * 60 * 60)
        self.add_cleanup("Save caches", self.cache.save)

        # Docker for Mac only shares some folders; the default TMPDIR
        # on OS X is not one of them, so make sure we use /tmp:
        self.temp = Path(mkdtemp(prefix="tel-", dir="/tmp"))
        (self.temp / "session_id.txt").write_text(self.session_id)
        self.add_cleanup("Remove temporary directory", rmtree, self.temp)

        # Adjust PATH to cover common locations for conntrack, ifconfig, etc.
        # Also maybe prepend Telepresence's libexec directory.
        path = os.environ.get("PATH", os.defpath)
        path_elements = path.split(os.pathsep)
        for additional in "/usr/sbin", "/sbin":
            if additional not in path_elements:
                path += ":" + additional
        libexec = TELEPRESENCE_BINARY.parents[1] / "libexec"
        if libexec.exists():
            path = "{}:{}".format(libexec, path)
        os.environ["PATH"] = path

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

    def show(self, message: str) -> None:
        """Display a message to the user on stderr"""
        self.write(message, prefix=">>>")
        for line in message.splitlines():
            print(self.wrapper.fill(line), file=sys.stderr)

    def show_raw(self, message: str) -> None:
        """Display a message to the user on stderr (no reformatting)"""
        self.write(message, prefix=">>>")
        for line in message.splitlines():
            print(self.raw_wrapper.fill(line), file=sys.stderr)

    def make_temp(self, name: str) -> Path:
        res = self.temp / name
        res.mkdir()
        return res

    # Privilege escalation (sudo)

    def _hold_sudo(self) -> None:
        counter = 0
        while self.sudo_held:
            # Sleep between calls to sudo
            if counter < 30:
                sleep(1)
                counter += 1
            else:
                try:
                    self.check_call(["sudo", "-n", "echo", "-n"])
                    counter = 0
                except CalledProcessError:
                    self.sudo_held = False
                    self.write("Attempt to hold on to sudo privileges failed")
        self.write("(sudo privileges holder thread exiting)")

    def _drop_sudo(self) -> None:
        self.sudo_held = False

    def require_sudo(self) -> None:
        """
        Grab sudo and hold on to it. Show a clear prompt to the user.
        """
        if self.sudo_held:
            return

        try:
            # See whether we can grab privileges without a password
            self.check_call(["sudo", "-n", "echo", "-n"])
        except CalledProcessError:
            # Apparently not. Prompt clearly then sudo again.
            self.show("Invoking sudo. Please enter your sudo password.")
            try:
                self.check_call(["sudo", "echo", "-n"])
            except CalledProcessError:
                raise self.fail("Unable to escalate privileges with sudo")

        self.sudo_held = True
        Thread(target=self._hold_sudo).start()
        self.add_cleanup("Kill sudo privileges holder", self._drop_sudo)

    # Dependencies

    def depend(self, commands: typing.Iterable[str]) -> typing.List[str]:
        """
        Find unavailable commands from a set of dependencies
        """
        return [command for command in commands if which(command) is None]

    def require(self, commands: typing.Iterable[str], message: str) -> None:
        """
        Verify that a set of dependencies (commands that can be called from the
        shell) are available. Fail with an explanation if any is unavailable.
        """
        missing = self.depend(commands)
        if missing:
            self.show("Required dependencies not found in your PATH:")
            self.show("  {}".format(" ".join(missing)))
            self.show(message)
            raise self.fail(
                "Please see " +
                "https://www.telepresence.io/reference/install#dependencies " +
                "for more information."
            )

    # Time

    def time(self) -> float:
        """
        Return the time in seconds since the epoch.
        """
        return time()

    def sleep(self, seconds: float) -> None:
        """
        Suspend execution for the given number of seconds.
        """
        sleep(seconds)

    def loop_until(self, loop_seconds: float,
                   sleep_seconds: float) -> typing.Iterable[int]:
        """
        Yield a loop counter during the loop time, then end. Sleep the
        specified amount between loops. Always run at least once. Check for
        background process early exit while looping.

        :param loop_seconds: How long the loop should run
        :param sleep_seconds: How long to sleep between loops
        :return: yields the loop counter, 0 onward
        """
        end_time = self.time() + loop_seconds - sleep_seconds
        counter = 0
        while True:
            yield counter
            if self.quitting:
                self.bg_process_crash()
                # Not reached
            counter += 1
            if self.time() >= end_time:
                break
            self.sleep(sleep_seconds)

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

    def _run_command(self, track, msg1, msg2, out_cb, err_cb, args, **kwargs):
        """Run a command synchronously"""
        self.output.write("[{}] {}: {}".format(track, msg1, str_command(args)))
        span = self.span(
            "{} {}".format(track, str_command(args))[:80],
            False,
            verbose=False
        )
        try:
            process = _launch_command(args, out_cb, err_cb, **kwargs)
        except OSError as exc:
            self.output.write("[{}] {}".format(track, exc))
            raise
        retcode = process.wait()
        spent = span.end()
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

    def launch(
        self, name: str, args, killer=None, keep_session=False, **kwargs
    ) -> None:
        if not keep_session:
            # This prevents signals from getting forwarded, but breaks sudo
            # if it is configured to ask for a password.
            kwargs["start_new_session"] = True
        assert "stderr" not in kwargs
        kwargs["stderr"] = STDOUT
        self.counter = track = self.counter + 1
        capture = deque(maxlen=10)  # type: typing.MutableSequence[str]
        out_cb = err_cb = self._make_logger(track, capture=capture)

        def done(proc):
            retcode = proc.wait()
            self.output.write("[{}] exit {}".format(track, retcode))
            self.quitting = True
            recent_lines = [str(line) for line in capture if line is not None]
            recent = "  ".join(recent_lines).strip()
            if recent:
                recent = "\nRecent output was:\n  {}".format(recent)
            message = (
                "Background process ({}) exited with return code {}. "
                "Command was:\n  {}\n{}"
            ).format(name, retcode, str_command(args), recent)
            self.ended.append(message)

        self.output.write(
            "[{}] Launching {}: {}".format(track, name, str_command(args))
        )
        try:
            process = _launch_command(
                args, out_cb, err_cb, done=done, **kwargs
            )
        except OSError as exc:
            self.output.write("[{}] {}".format(track, exc))
            raise
        self.add_cleanup(
            "Kill BG process [{}] {}".format(track, name),
            killer if killer else partial(kill_process, process),
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

    def bg_process_crash(self) -> None:
        """
        Invoke the crash reporter, emitting additional information about the
        background process early exit(s) that prompted this crash.
        """
        self.quitting = True  # should be a no-op
        message = "{} background process(es) crashed".format(len(self.ended))
        failures = "\n\n".join(self.ended)
        raise BackgroundProcessCrash(message, failures)

    def fail(self, message: str, code=1) -> SystemExit:
        """
        Report failure to the user and exit. Does not return. Cleanup will run
        before the process ends. This does not invoke the crash reporter; an
        uncaught exception will achieve that, e.g., RuntimeError.

        :param message: So the user knows what happened
        :param code: Process exit code
        """
        self.quitting = True
        self.show(message)
        self.write("EXITING with status code {}".format(code))
        exit(code)
        return SystemExit(code)  # Not reached; just here for the linters

    def exit(self) -> SystemExit:
        """
        Exit after a successful session. Does not return. Cleanup will run
        before the process ends.
        """
        self.quitting = True
        Span.emit_summary = True
        self.write("EXITING successful session.")
        exit(0)
        return SystemExit(0)  # Not reached; just here for the linters

    def wait_for_exit(self, main_process: Popen) -> None:
        """
        Monitor main process and background items until done
        """
        self.write("Everything launched. Waiting to exit...")
        main_code = None
        span = self.span()
        while not self.quitting and main_code is None:
            sleep(0.1)
            main_code = main_process.poll()
        span.end()

        if main_code is not None:
            # Shell exited, we're done. Automatic shutdown cleanup
            # will kill subprocesses.
            main_command = str_command(str(arg) for arg in main_process.args)
            self.write("Main process ({})".format(main_command))
            self.write(" exited with code {}.".format(main_code))
            raise self.exit()

        # Something else exited, setting the quitting flag.
        # Unfortunately torsocks doesn't deal well with connections
        # being lost, so best we can do is shut down.
        if self.ended:
            self.show("\n")
            self.show_raw(self.ended[0])
        self.show("\n")
        message = (
            "Proxy to Kubernetes exited. This is typically due to"
            " a lost connection."
        )
        raise self.fail(message, code=3)
