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

from subprocess import DEVNULL, PIPE, Popen
from threading import Thread


class BackgroundProcessCrash(Exception):
    def __init__(self, message: str, details: str) -> None:
        super().__init__(message)
        self.details = details


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
