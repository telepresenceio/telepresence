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
import platform
import select
import signal
import socket
import sys
import textwrap
import typing
import uuid
from contextlib import contextmanager
from functools import partial
from inspect import currentframe, getframeinfo
from pathlib import Path
from shutil import rmtree, which
from subprocess import STDOUT, CalledProcessError, Popen, TimeoutExpired
from tempfile import mkdtemp
from threading import Thread
from time import sleep, time

from telepresence import TELEPRESENCE_BINARY
from telepresence.utilities import kill_process, str_command

from .cache import Cache
from .kube import KUBE_UNSET
from .launch import BackgroundProcessCrash, _launch_command, _Logger
from .output import Output
from .output_mask import mask_sensitive_data
from .span import Span

_CleanupItem = typing.NamedTuple(
    "_CleanupItem", [
        ("name", str),
        ("callable", typing.Callable[..., None]),
        ("args", typing.Tuple[typing.Any, ...]),
        ("kwargs", typing.Dict[str, typing.Any]),
    ]
)


class Runner:
    """Context for running subprocesses."""
    def __init__(self, logfile_path: str, verbose: bool) -> None:
        """
        :param logfile_path: Path or string file path or "-" for stdout
        :param kubeinfo: How to run kubectl or equivalent
        :param verbose: Whether subcommand should run in verbose mode.
        """
        self.output = Output(logfile_path)
        self.logfile_path = self.output.logfile_path
        self.kubectl = KUBE_UNSET
        self.verbose = verbose
        self.start_time = time()
        self.current_span = None  # type: typing.Optional[Span]
        self.counter = 0
        self.cleanup_stack = []  # type: typing.List[_CleanupItem]
        self.sudo_held = False
        self.sudo_for_docker = False
        self.quitting = False
        self.ended = []  # type: typing.List[str]

        self.is_wsl = False
        if sys.platform.startswith("linux"):
            self.platform = "linux"

            # Detect if this platform is really linux-on-windows
            if platform.uname().release.endswith("-Microsoft"):
                self.is_wsl = True
        elif sys.platform.startswith("darwin"):
            self.platform = "darwin"
        else:
            # For untested platforms...
            self.platform = sys.platform
        self.output.write("uname: {}".format(platform.uname()))
        self.output.write("Platform: {}".format(self.platform))
        self.output.write("WSL: {}".format(self.is_wsl))

        term_width = 99999
        self.chatty = False
        if sys.stderr.isatty():
            err_fd = sys.stderr.fileno()
            try:
                term_width = os.get_terminal_size(err_fd).columns - 1
                self.chatty = True
            except OSError:
                pass
        if term_width < 25:
            term_width = 99999
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

        cache_dir = os.path.expanduser("~/.cache/telepresence")
        os.makedirs(cache_dir, exist_ok=True)
        cache_filename = os.path.join(cache_dir, "cache.json")
        self.cache = Cache.load(cache_filename)
        self.cache.invalidate(12 * 60 * 60)
        self.add_cleanup("Save caches", self.cache.save, cache_filename)

        # Docker for Mac doesn't share TMPDIR, so make sure we use /tmp
        # Docker for Windows can't access /tmp, so use a directory it can
        tmp_dir = "/tmp"
        if self.is_wsl:
            c_drive = "/mnt/c"
            if not os.path.exists(c_drive):
                c_drive = "/c"
            tmp_dir = os.path.join(c_drive, "Temp")
        if not os.path.exists(tmp_dir):
            os.makedirs(tmp_dir)
        self.temp = Path(mkdtemp(prefix="tel-", dir=tmp_dir))
        (self.temp / "session_id.txt").write_text(self.session_id)
        self.add_cleanup("Remove temporary directory", rmtree, str(self.temp))

        # Adjust PATH to cover common locations for conntrack, ifconfig, etc.
        # Also maybe prepend Telepresence's libexec directory.
        path = os.environ.get("PATH", os.defpath)
        path_elements = path.split(os.pathsep)
        for additional in "/usr/sbin", "/sbin":
            if additional not in path_elements:
                path += ":" + additional
        try:
            libexec = TELEPRESENCE_BINARY.parents[1] / "libexec"
        except IndexError:
            libexec = TELEPRESENCE_BINARY / "does_not_exist_please"
        if libexec.exists():
            path = "{}:{}".format(libexec, path)
        os.environ["PATH"] = path

    def span(
        self,
        name: str = "",
        context: bool = True,
        verbose: bool = True
    ) -> Span:
        """Write caller's frame info to the log."""

        if context:
            frame = currentframe()
            assert frame is not None  # mypy
            assert frame.f_back is not None  # mypy
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

    def write(self, message: str, prefix: str = "TEL") -> None:
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

        self.require(["sudo"], "Some operations require elevated privileges")
        try:
            # See whether we can grab privileges without a password
            self.check_call(["sudo", "-n", "echo", "-n"])
        except CalledProcessError:
            # Apparently not. Prompt clearly then sudo again.
            self.show(
                "How Telepresence uses sudo: " +
                "https://www.telepresence.io/reference/install#dependencies"
            )
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
        missing = []
        for command in commands:
            path = which(command)
            if path:
                self.write("Found {} -> {}".format(command, path))
            else:
                missing.append(command)
        return missing

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

    def require_docker(self) -> None:
        self.require(["docker"], "Needed for the container method.")

        # Check whether `sudo docker` is required.
        # FIXME(ark3): This assumes a local docker. We check for that in a
        # roundabout way elsewhere. Consider using `docker context inspect` to
        # do all of this stuff in a way that may be supported.
        dsock = "/var/run/docker.sock"
        if os.path.exists(dsock) and not os.access(dsock, os.W_OK):
            self.require_sudo()
            self.sudo_for_docker = True

    def docker(self, *args: str, env: bool = False) -> typing.List[str]:
        if not self.sudo_for_docker:
            return ["docker"] + list(args)
        if env:
            return ["sudo", "-E", "docker"] + list(args)
        return ["sudo", "docker"] + list(args)

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

    def _make_logger(
        self, track: int, do_log: bool, do_capture: bool, capture_limit: int
    ) -> _Logger:
        """Create a logger that optionally captures what is logged"""
        prefix = "{:>3d}".format(track)

        def write(line: str) -> None:
            self.output.write(mask_sensitive_data(line), prefix=prefix)

        return _Logger(write, do_log, do_capture, capture_limit)

    def _run_command_sync(
        self,
        messages: typing.Tuple[str, str],
        log_stdout: bool,
        stderr_to_stdout: bool,
        args: typing.List[str],
        capture_limit: int,
        timeout: typing.Optional[float],
        input: typing.Optional[bytes],
        env: typing.Optional[typing.Dict[str, str]],
    ) -> str:
        """
        Run a command synchronously. Log stdout (optionally) and stderr (if not
        redirected to stdout). Capture stdout and stderr, at least for
        exceptions. Return output.
        """
        self.counter = track = self.counter + 1
        self.output.write(
            "[{}] {}: {}".format(track, messages[0], str_command(args))
        )
        span = self.span(
            "{} {}".format(track, str_command(args))[:80],
            False,
            verbose=False
        )
        kwargs = {}  # type: typing.Dict[str, typing.Any]
        if env is not None:
            kwargs["env"] = env
        if input is not None:
            kwargs["input"] = input

        # Set up capture/logging
        out_logger = self._make_logger(
            track, log_stdout or self.verbose, True, capture_limit
        )
        if stderr_to_stdout:
            # This logger won't be used
            err_logger = self._make_logger(track, False, False, capture_limit)
            kwargs["stderr"] = STDOUT
        else:
            err_logger = self._make_logger(track, True, True, capture_limit)

        # Launch the process and wait for it to finish
        try:
            process = _launch_command(args, out_logger, err_logger, **kwargs)
        except OSError as exc:
            # Failed to launch, so no need to wrap up capture stuff.
            self.output.write("[{}] {}".format(track, exc))
            raise

        TIMED_OUT_RETCODE = -999
        try:
            retcode = process.wait(timeout)
        except TimeoutExpired:
            retcode = TIMED_OUT_RETCODE  # sentinal for timeout
            process.terminate()
            try:
                process.wait(timeout=1)
            except TimeoutExpired:
                process.kill()
                process.wait()

        output = out_logger.get_captured()
        spent = span.end()

        if retcode == TIMED_OUT_RETCODE:
            # Command timed out. Need to raise TE.
            self.output.write(
                "[{}] timed out after {:0.2f} secs.".format(track, spent)
            )
            assert timeout is not None
            raise TimeoutExpired(
                args,
                timeout,
                output,
                None if stderr_to_stdout else err_logger.get_captured(),
            )
        if retcode:
            # Command failed. Need to raise CPE.
            self.output.write(
                "[{}] exit {} in {:0.2f} secs.".format(track, retcode, spent)
            )
            raise CalledProcessError(
                retcode,
                args,
                output,
                None if stderr_to_stdout else err_logger.get_captured(),
            )

        # Command succeeded. Just return the output
        self.output.write(
            "[{}] {} in {:0.2f} secs.".format(track, messages[1], spent)
        )
        return output

    def check_call(
        self,
        args: typing.List[str],
        timeout: typing.Optional[float] = None,
        input: typing.Optional[bytes] = None,
        env: typing.Optional[typing.Dict[str, str]] = None,
    ) -> None:
        """Run a subprocess, make sure it exited with 0."""
        self._run_command_sync(
            ("Running", "ran"),
            True,
            False,
            args,
            10,  # limited capture, only used for error reporting
            timeout,
            input,
            env,
        )

    def get_output(
        self,
        args: typing.List[str],
        timeout: typing.Optional[float] = None,
        stderr_to_stdout: bool = False,
        reveal: bool = False,
        input: typing.Optional[bytes] = None,
        env: typing.Optional[typing.Dict[str, str]] = None,
    ) -> str:
        """Return (stripped) command result as unicode string."""
        output = self._run_command_sync(
            ("Capturing", "captured"),
            reveal,
            stderr_to_stdout,
            args,
            -1,  # unlimited capture
            timeout,
            input,
            env,
        )
        return output

    def launch(
        self,
        name: str,
        args: typing.List[str],
        killer: typing.Optional[typing.Callable[[], None]] = None,
        notify: bool = False,
        keep_session: bool = False,
        bufsize: int = -1,
        is_critical: bool = True,
    ) -> None:
        """Asyncrounously run a process.

        :param name: A human-friendly name to describe the process.

        :param args: The command to run.

        :param killer: How to signal to the process that it should
        stop.  The default is to call Popen.terminate(), which on
        POSIX OSs sends SIGTERM.

        :param notify: Whether to synchronously wait for the process
        to send "READY=1" via the ``sd_notify(3)`` interface before
        returning.

        :param keep_session: Whether to run the process in the current
        session (as in ``setsid()``), or in a new session.  The
        default is to run in a new session, in order to prevent
        keyboard signals from getting forwarded.  However, running in
        a new session breaks sudo if it is configured to ask for a
        password.

        :parmam bufsize: See ``subprocess.Popen()`.

        :param is_critical: Whether this process quitting should end this
        Telepresence session. Default is True because that used to be the
        only supported behavior.

        :return: ``None``.

        """
        self.counter = track = self.counter + 1
        out_logger = self._make_logger(track, True, True, 10)

        def done(proc: "Popen[str]") -> None:
            retcode = proc.wait()
            self.output.write("[{}] {}: exit {}".format(track, name, retcode))
            recent = "\n  ".join(out_logger.get_captured().split("\n"))
            if recent:
                recent = "\nRecent output was:\n  {}".format(recent)
            message = (
                "Background process ({}) exited with return code {}. "
                "Command was:\n  {}\n{}"
            ).format(name, retcode, str_command(args), recent)
            self.ended.append(message)
            if is_critical:
                # End the program because this is a critical subprocess
                self.quitting = True
            else:
                # Record the failure but don't quit
                self.output.write(message)

        self.output.write(
            "[{}] Launching {}: {}".format(track, name, str_command(args))
        )
        env = os.environ.copy()
        if notify:
            sockname = str(self.temp / "notify-{}".format(track))
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
            sock.bind(sockname)
            env["NOTIFY_SOCKET"] = sockname
        try:
            process = _launch_command(
                args,
                out_logger,
                out_logger,  # Won't be used
                done=done,
                # kwargs
                start_new_session=not keep_session,
                stderr=STDOUT,
                bufsize=bufsize,
                env=env
            )
        except OSError as exc:
            self.output.write("[{}] {}".format(track, exc))
            raise
        if killer is None:
            killer = partial(kill_process, process)
        self.add_cleanup("Kill BG process [{}] {}".format(track, name), killer)
        if notify:
            # We need a select()able notification of death in case the
            # process dies before sending READY=1.  In C, I'd do this
            # same pipe trick, but close the pipe from a SIGCHLD
            # handler, which is lighter than a thread.  But I fear
            # that a SIGCHLD handler would interfere with the Python
            # runtime?  We're already using several threads per
            # launched process, so what's the harm in one more?
            pr, pw = os.pipe()

            def pipewait() -> None:
                process.wait()
                os.close(pw)

            Thread(target=pipewait, daemon=True).start()

            # Block until either the process exits or we get a READY=1
            # line on the socket.
            while process.poll() is None:
                r, _, x = select.select([pr, sock], [], [pr, sock])
                if sock in r or sock in x:
                    lines = sock.recv(4096).decode("utf-8").split("\n")
                    if "READY=1" in lines:
                        break

            os.close(pr)
            sock.close()

    # Cleanup

    def add_cleanup(
        self, name: str, callback: typing.Callable[..., None],
        *args: typing.Any, **kwargs: typing.Any
    ) -> None:
        """
        Set up callback to be called during cleanup processing on exit.

        :param name: Logged for debugging
        :param callback: What to call during cleanup
        """
        cleanup_item = _CleanupItem(name, callback, args, kwargs)
        self.cleanup_stack.append(cleanup_item)

    def _signal_received(self, sig_num: int, frame: typing.Any) -> None:
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
        self.exit(0)

    def _do_cleanup(self) -> typing.List[typing.Tuple[str, BaseException]]:
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
    def cleanup_handling(self) -> typing.Iterator[None]:
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

    def fail(self, message: str) -> SystemExit:
        """
        Report failure to the user and exit. Does not return. Cleanup will run
        before the process ends. This does not invoke the crash reporter; an
        uncaught exception will achieve that, e.g., RuntimeError.

        Failure is indicated with exit code 255 (like ssh). The user process's
        exit code is propagated by successful sessions.

        :param message: So the user knows what happened
        """
        self.quitting = True
        self.show("\n")
        self.show(message)
        self.show("\n")
        code = 255
        self.write("EXITING with status code {}".format(code))
        exit(code)
        return SystemExit(code)  # Not reached; just here for the linters

    def exit(self, code: int) -> SystemExit:
        """
        Exit after a successful session. Does not return. Cleanup will run
        before the process ends.

        Success means exiting with the user process's exit code.
        """
        self.quitting = True
        Span.emit_summary = True
        self.write("EXITING successful session.")
        exit(code)
        return SystemExit(code)  # Not reached; just here for the linters

    def wait_for_exit(self, main_process: "Popen[str]") -> None:
        """
        Monitor main process and background items until done
        """
        main_code = None

        def wait_for_process(p: "Popen[str]") -> None:
            """Wait for process and set main_code and self.quitting flag

            Note that main_code is defined in the parent function,
            so it is declared as nonlocal

            See https://github.com/telepresenceio/telepresence/issues/1003
            """
            nonlocal main_code
            main_code = p.wait()
            main_command = str_command(str(arg) for arg in main_process.args)
            self.write("Main process ({})".format(main_command))
            self.write(" exited with code {}.".format(main_code))
            self.quitting = True

        self.write("Everything launched. Waiting to exit...")
        span = self.span()
        Thread(target=wait_for_process, args=(main_process, )).start()
        while not self.quitting:
            sleep(0.1)
        span.end()

        if main_code is not None:
            # User process exited, we're done. Automatic shutdown cleanup
            # will kill subprocesses.
            message = "Your process "
            if main_code:
                message += "exited with return code {}.".format(main_code)
            else:
                message += "has exited."
            self.show(message)
            raise self.exit(main_code)

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
        raise self.fail(message)
