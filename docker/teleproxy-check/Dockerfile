FROM golang:1.11-alpine
RUN apk --no-cache add make iptables sudo git

WORKDIR /root/teleproxy
ADD teleproxy.tar .

ENV CGO_ENABLED=0
ENV GOFLAGS=-mod=vendor
