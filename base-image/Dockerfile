# Copyright 2020-2021 Datawire. All rights reserved.
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

FROM alpine:3.13 as tel2-base
RUN \
  apk add --no-cache openssh-sftp-server && \
  mkdir /home/telepresence && \
  mkdir /tel_app_mounts && \
  chmod 0770 /tel_app_mounts

# Make symlinks so we can use these commands directly in k8s.
RUN \
  ln -s /ko-app/traffic /usr/local/bin/traffic-manager && \
  ln -s /ko-app/traffic /usr/local/bin/traffic-agent

# Clear the default command to avoid passing bogus arguments to traffic
CMD []
