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

import typing
from collections import deque
from subprocess import DEVNULL, PIPE, Popen
from threading import Lock, Thread


class BackgroundProcessCrash(Exception):
    def __init__(self, message: str, details: str) -> None:
        super().__init__(message)
        self.details = details


class _Logger:
    """Logger that optionally captures what is logged"""

    def __init__(
        self,
        write: typing.Callable[[str], None],
        do_log: bool,
        do_capture: bool,
        max_capture: int,
    ):
        self.write = write
        self.do_log = do_log
        self.do_capture = do_capture
        self.finished = Lock()
        self.capture = deque(
            maxlen=max_capture
        )  # type: typing.MutableSequence[str]
        self.finished.acquire()

    def log(self, line: str) -> None:
        if self.do_log:
            self.write(line)
        if self.do_capture:
            self.capture.append(line)

    def finish(self) -> None:
        self.finished.release()

    def get_captured(self) -> str:
        self.finished.acquire()  # Block until finish is called
        return "".join(self.capture).strip()


def _launch_command(
    args: typing.List[str],
    out_logger: _Logger,
    err_logger: _Logger,
    done: typing.Optional[typing.Callable[[Popen], None]] = None,
    **kwargs: typing.Any
) -> Popen:
    """
    Launch subprocess with args, kwargs.
    Log stdout and stderr by calling respective callbacks.
    """

    def pump_stream(logger: _Logger, stream: typing.Iterable[str]) -> None:
        """Pump the stream"""
        for line in stream:
            logger.log(line)
        logger.finish()

    def joiner() -> None:
        """Wait for streams to finish, then call done callback"""
        for th in threads:
            th.join()
        if done:
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
            target=pump_stream, args=(out_logger, process.stdout), daemon=True
        )
        thread.start()
        threads.append(thread)
    if process.stderr:
        thread = Thread(
            target=pump_stream, args=(err_logger, process.stderr), daemon=True
        )
        thread.start()
        threads.append(thread)
    if done and threads:
        Thread(target=joiner, daemon=True).start()
    if in_data:
        process.stdin.write(str(in_data, "utf-8"))
        process.stdin.close()
    return process
