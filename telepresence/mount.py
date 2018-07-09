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

import sys
from pathlib import Path
from subprocess import STDOUT, CalledProcessError
from tempfile import mkdtemp
from typing import Tuple, Callable

from telepresence.runner import Runner
from telepresence.ssh import SSH


def mount_remote_volumes(
    runner: Runner, ssh: SSH, allow_all_users: bool, mount_dir: str
) -> Tuple[str, Callable]:
    """
    sshfs is used to mount the remote system locally.

    Allowing all users may require root, so we use sudo in that case.

    Returns (path to mounted directory, callable that will unmount it).
    """
    span = runner.span()
    sudo_prefix = ["sudo"] if allow_all_users else []
    middle = ["-o", "allow_other"] if allow_all_users else []
    try:
        runner.get_output(
            sudo_prefix + [
                "sshfs",
                "-p",
                str(ssh.port),
                # Don't load config file so it doesn't break us:
                "-F",
                "/dev/null",
                # Don't validate host key:
                "-o",
                "StrictHostKeyChecking=no",
                # Don't store host key:
                "-o",
                "UserKnownHostsFile=/dev/null",
            ] + middle + ["telepresence@localhost:/", mount_dir],
            stderr=STDOUT
        )
        mounted = True
    except CalledProcessError as exc:
        runner.show(
            "Mounting remote volumes failed, they will be unavailable"
            " in this session. If you are running"
            " on Windows Subystem for Linux then see"
            " https://github.com/datawire/telepresence/issues/115,"
            " otherwise please report a bug, attaching telepresence.log to"
            " the bug report:"
            " https://github.com/datawire/telepresence/issues/new"
        )
        if exc.output:
            runner.show("\nMount error was: {}\n".format(exc.output.strip()))
        mounted = False

    def no_cleanup():
        pass

    def cleanup():
        if sys.platform.startswith("linux"):
            runner.check_call(
                sudo_prefix + ["fusermount", "-z", "-u", mount_dir]
            )
        else:
            runner.get_output(sudo_prefix + ["umount", "-f", mount_dir])
        try:
            Path(mount_dir).rmdir()
        except OSError:
            pass

    span.end()
    return mount_dir, cleanup if mounted else no_cleanup


def mount_remote(session):
    """Handle filesystem stuff (pod name, ssh object)"""
    args = session.args
    if args.mount:
        # The mount directory is made here, removed by mount_cleanup if
        # mount succeeds, leaked if mount fails.
        if args.mount is True:
            # Docker for Mac only shares some folders; the default TMPDIR
            # on OS X is not one of them, so make sure we use /tmp:
            mount_dir = mkdtemp(dir="/tmp")
        else:
            # FIXME: Maybe warn if args.mount doesn't start with /tmp?
            try:
                args.mount.mkdir(parents=True, exist_ok=True)
            except OSError as exc:
                raise session.runner.fail(
                    "Unable to use mount path: {}".format(exc)
                )
            mount_dir = str(args.mount)
        # We allow all users if we're using Docker because we don't know
        # what uid the Docker container will use.
        mount_dir, mount_cleanup = mount_remote_volumes(
            session.runner,
            session.ssh,
            args.method == "container",  # allow all users
            mount_dir,
        )
        session.env["TELEPRESENCE_ROOT"] = mount_dir
        session.runner.add_cleanup("Unmount remote filesystem", mount_cleanup)
    else:
        mount_dir = None
    return mount_dir
