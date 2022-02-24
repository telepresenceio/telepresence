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
FROM golang:alpine3.15 as tel2-base

RUN apk add git gcc make cmake musl-nscd-dev libc-dev
RUN git clone git://git.samba.org/nss_wrapper.git

WORKDIR nss_wrapper

RUN git checkout nss_wrapper-1.1.11 && \
	mkdir obj && \
    cd obj && \
	cmake -DCMAKE_INSTALL_PREFIX=/usr/local .. && \
    make && \
    cd src && \
    mkdir /nss_wrapper && \
    tar -f /nss_wrapper/libs.tar.gz -cz lib*

COPY base-image/passwd.template /nss_wrapper
COPY base-image/startscript.sh /nss_wrapper

FROM tel2-base AS tel2-build

WORKDIR telepresence
COPY go.mod .
COPY go.sum .
COPY rpc/go.mod rpc/
COPY rpc/go.sum rpc/

# Get dependancies - will also be cached if we won't change mod/sum
RUN go mod download

COPY cmd/ cmd/
COPY pkg/ pkg/
COPY rpc/ rpc/
COPY build-output/version.txt .

RUN go install -trimpath -ldflags=-X=$(go list ./pkg/version).Version=$(cat version.txt) ./cmd/traffic/...
RUN cp $(go env GOPATH)/bin/traffic /usr/local/bin

FROM alpine:3.15 as tel2

RUN apk add --no-cache openssh-sftp-server ca-certificates iptables

COPY --from=tel2-base /nss_wrapper/libs.tar.gz .
RUN tar -f libs.tar.gz -C /usr/local/lib -xz && rm libs.tar.gz

# the traffic binary
COPY --from=tel2-base /nss_wrapper/passwd.template .
COPY --from=tel2-base /nss_wrapper/startscript.sh .
COPY --from=tel2-build /usr/local/bin/traffic /usr/local/bin

RUN \
  mkdir /tel_app_mounts && \
  chgrp -R 0 /tel_app_mounts && \
  chmod -R g=u /tel_app_mounts && \
  mkdir -p /home/telepresence && \
  chmod 0777 /home/telepresence

RUN \
  chgrp -R 0 startscript.sh && \
  chmod -R g=u startscript.sh && \
  chmod -R +x startscript.sh

ENV \
 USER_NAME=telepresence \
 NSS_WRAPPER_PASSWD=/tmp/passwd \
 NSS_WRAPPER_GROUP=/etc/group

ENTRYPOINT ["/startscript.sh", "traffic"]
CMD []
