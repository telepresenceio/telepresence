FROM golang:alpine3.15 AS builder

WORKDIR /udp-echo
COPY go.mod .
COPY main.go .
RUN go build -o udp-echo .

FROM alpine:3.15
COPY --from=builder /udp-echo/udp-echo /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/udp-echo"]
