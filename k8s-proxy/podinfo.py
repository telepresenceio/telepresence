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
"""
Emit information about the pod as a JSON blob
"""

import json
import os
import re

IGNORED_MOUNTS = [
    r'/sys($|/.*)', r'/proc($|/.*)', r'/dev($|/.*)', r'/etc/hostname$',
    r'/etc/resolv.conf$', r'/etc/hosts$', r'/$'
]


def get_mount_points():
    "Returns a filtered list of mount-points"
    ret = []

    ignore = re.compile('(' + '|'.join(IGNORED_MOUNTS) + ')')
    splitter = re.compile(r'\s+')

    try:
        with open('/proc/mounts', 'r') as mount_fp:
            for line in mount_fp:
                mount_point = splitter.split(line)[1]
                if not ignore.match(mount_point):
                    ret.append(mount_point)
    except IOError:
        return []

    return ret


print(
    json.dumps(
        dict(
            env=dict(os.environ),
            hostname=open("/etc/hostname").read(),
            resolv=open("/etc/resolv.conf").read(),
            mountpoints=get_mount_points(),
        )
    )
)
