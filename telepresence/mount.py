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

from subprocess import CalledProcessError
from typing import Callable, Tuple
import random
import string

from telepresence.connect import SSH
from telepresence.runner import Runner


def mount_remote_volumes(
    runner: Runner, ssh: SSH, allow_all_users: bool, use_docker_volume: bool,
    mount_dir: str
) -> Tuple[str, Callable]:
    """
    sshfs is used to mount the remote system locally.

    Allowing all users may require root, so we use sudo in that case.

    Returns (path to mounted directory, callable that will unmount it).
    """
    span = runner.span()
    try:
        if use_docker_volume:
            ssh_args = ssh.required_args.copy()
            f_index = ssh_args.index("-F") if "-F" in ssh_args else None
            if f_index is not None:
                del ssh_args[f_index + 1]
                del ssh_args[f_index]

            runner.check_call([
                "docker", "volume", "create", "-d", "vieux/sshfs", "-o",
                "port=" + str(ssh.port)
            ] + ssh_args + [
                "-o", "allow_other", "-o", "sshcmd=" +
                "{}:/".format(ssh.user_at_host), mount_dir
            ])
        else:
            if allow_all_users:
                sudo_prefix = ["sudo"]
                middle = ["-o", "allow_other"]
            else:
                sudo_prefix = []
                middle = []

            runner.check_call(
                sudo_prefix + ["sshfs", "-p", str(ssh.port)] +
                ssh.required_args + middle +
                ["{}:/".format(ssh.user_at_host), mount_dir],
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
        if exc.stderr:
            runner.show("\nMount error was: {}\n".format(exc.stderr.strip()))
        mounted = False

    def no_cleanup():
        pass

    def cleanup():
        if use_docker_volume:
            runner.check_call(["docker", "volume", "rm", "-f", mount_dir])
        elif runner.platform == "linux":
            runner.check_call(
                sudo_prefix + ["fusermount", "-z", "-u", mount_dir]
            )
        else:
            runner.check_call(sudo_prefix + ["umount", "-f", mount_dir])

    span.end()
    return mount_dir, cleanup if mounted else no_cleanup


def mount_remote(runner, mount, ssh, allow_all_users, docker_mount, env):
    """Handle filesystem stuff (pod name, ssh object)"""
    if mount or docker_mount:
        # The mount directory is made here, removed by mount_cleanup if
        # mount succeeds, leaked if mount fails.
        if docker_mount:
            mount_dir = "telepresence-" + ''.join(
                random.choice(string.ascii_lowercase) for i in range(9)
            )
            mount_target = str(docker_mount)
        elif mount is True:
            mount_dir = str(runner.make_temp("fs"))
            mount_target = mount_dir
        else:
            # Try to create the mount point as a sanity check. If we do create
            # it, we leave it behind. This is sort of a leak. Kind of.
            # FIXME: Maybe warn if mount doesn't start with /tmp?
            try:
                mount.mkdir(parents=True, exist_ok=True)
            except OSError as exc:
                raise runner.fail("Unable to use mount path: {}".format(exc))
            mount_dir = str(mount)
            mount_target = mount_dir

        mount_dir, mount_cleanup = mount_remote_volumes(
            runner, ssh, allow_all_users, docker_mount != None, mount_dir
        )

        env["TELEPRESENCE_ROOT"] = mount_dir
        runner.add_cleanup("Unmount remote filesystem", mount_cleanup)
    else:
        mount_dir = None
        mount_target = None
    return mount_dir, mount_target


def setup(runner, args):
    """
    Set up one of three mount_remote implementations:
    - Do nothing
    - Mount onto a temporary directory
    - Mount onto a specified mount point
    """

    # We allow all users if we're using Docker and not using docker volume because we don't know
    # what uid the Docker container will use.
    allow_all_users = args.mount and args.method == "container"
    if not args.docker_mount and allow_all_users:
        runner.require_sudo()

    if not args.docker_mount and args.mount:
        needed = ["sshfs"]
        if runner.platform == "linux":
            needed.append("fusermount")
        else:
            needed.append("umount")
        runner.require(needed, "Required for volume mounts")

    if (args.mount or args.docker_mount) and runner.chatty:
        runner.show(
            "Volumes are rooted at $TELEPRESENCE_ROOT. See "
            "https://telepresence.io/howto/volumes.html for details."
        )
    return lambda runner_, env, ssh: mount_remote(
        runner_, args.mount, ssh, allow_all_users, args.docker_mount, env
    )
