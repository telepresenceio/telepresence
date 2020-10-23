# pling is executable build target
FROM golang:latest AS builder

RUN apt-get update; \
  apt-get install unzip

ENV HOME=/root
ENV GOPATH=$HOME/go
ENV GOBIN=$GOPATH/bin
ENV PATH=$GOBIN:$PATH

WORKDIR $GOPATH/src/github.com/datawire/telepresence2
COPY . .
RUN make install

FROM debian:buster-slim AS telepresence
COPY --from=builder /root/go/bin/telepresence .
ENTRYPOINT ["./telepresence"]

FROM debian:buster-slim AS trafficmanager
COPY --from=builder /root/go/bin/trafficmanager .
ENTRYPOINT ["./trafficmanager"]
