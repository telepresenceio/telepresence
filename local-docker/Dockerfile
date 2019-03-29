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

# docker build --file local-docker/Dockerfile .
FROM alpine:3.6

RUN apk add --no-cache python3 openssh iptables tini conntrack-tools git && \
    ssh-keygen -A && \
    echo -e "ClientAliveInterval 1\nGatewayPorts yes\nPermitEmptyPasswords yes\nPort 38022\nClientAliveCountMax 10\nPermitRootLogin yes\n" >> /etc/ssh/sshd_config && \
    pip3 install --upgrade pip && \
    pip3 install git+https://github.com/datawire/sshuttle.git@telepresence && \
    apk del --no-cache -r git && \
    passwd -d root

COPY setup.* versioneer.py /tmp/build/
COPY telepresence /tmp/build/telepresence
COPY local-docker/entrypoint.py /usr/bin/
RUN pip3 install /tmp/build && chmod +x /usr/bin/entrypoint.py
ENTRYPOINT ["/sbin/tini", "-v", "--", "python3", "/usr/bin/entrypoint.py"]
