# Copyright 2020-2022 Datawire. All rights reserved.
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

# The tel2-base target is reused by all telepresence cluster-side components. It's
# never published though, as it's quite large and all builds will access to this Dockerfile
FROM golang:alpine3.15 as tel2-base

RUN apk add --no-cache gcc musl-dev

WORKDIR telepresence
COPY go.mod .
COPY go.sum .
COPY rpc/go.mod rpc/
COPY rpc/go.sum rpc/

# Get dependencies - will also be cached if we won't change mod/sum
RUN go mod download

# The tel2-build target builds the traffic component, which is used both as a traffic-manager,
# agent init-container, and agent
FROM tel2-base AS tel2-build

COPY cmd/ cmd/
COPY pkg/ pkg/
COPY rpc/ rpc/
COPY build-output/version.txt .

RUN go install -trimpath -ldflags=-X=$(go list ./pkg/version).Version=$(cat version.txt) ./cmd/traffic/...
RUN cp $(go env GOPATH)/bin/traffic /usr/local/bin

# The tel2 targer is the one that gets published. It aims to be a small as possible.
FROM alpine:3.15 as tel2

RUN apk add --no-cache ca-certificates iptables

# the traffic binary
COPY --from=tel2-build /usr/local/bin/traffic /usr/local/bin

RUN \
  mkdir /tel_app_mounts && \
  chgrp -R 0 /tel_app_mounts && \
  chmod -R g=u /tel_app_mounts && \
  mkdir -p /home/telepresence && \
  chgrp -R 0 /home/telepresence && \
  chmod -R g=u /home/telepresence && \
  chmod 0777 /home/telepresence

ENTRYPOINT ["traffic"]
CMD []
