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

FROM alpine:3.6 as common

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY requirements.txt /usr/src/app

# For some reason pip doesn't install incremental (a Twisted dependency) so do
# so manually. When done, remove unneeded packages for a smaller image.
RUN apk add --no-cache python3 python3-dev openssh gcc libc-dev && \
    ssh-keygen -A && \
    echo -e "ClientAliveInterval 1\nGatewayPorts yes\nPermitEmptyPasswords yes\nPort 8022\nClientAliveCountMax 10\nPermitRootLogin yes\n" >> /etc/ssh/sshd_config && \
    pip3 install --no-cache-dir --upgrade pip && \
    pip3 install --no-cache-dir incremental && \
    pip3 install --no-cache-dir -r requirements.txt && \
    apk del --no-cache -r gcc libc-dev python3-dev

# Make a /usr/bin/python for sshuttle
RUN ln -s /usr/bin/python3 /usr/bin/python

COPY forwarder.py /usr/src/app
COPY socks.py /usr/src/app
COPY . /usr/src/app

#
# The normal (non-root) image
#
FROM common as telepresence-k8s

# Set up to run as the telepresence user (1000:0)
RUN chmod -R g+r /etc/ssh && \
    chmod -R g+w /usr/src/app && \
    echo "telepresence::1000:0:Telepresence User:/usr/src/app:/bin/ash" >> /etc/passwd
USER 1000:0

CMD /usr/src/app/pre-run.sh

#
# The privileged (running-as-root) image
#
FROM common as telepresence-k8s-priv

# Set up to run as the telepresence user (1000:0)
RUN echo "telepresence::0:0:Telepresence User:/usr/src/app:/bin/ash" >> /etc/passwd
#RUN chmod -R 0600 /etc/ssh/*

CMD /usr/src/app/run.sh
