FROM golang:1.25-alpine AS builder

WORKDIR /udpthroughput
COPY go.mod ./
COPY main.go .
RUN CGO_ENABLED=0 go build -o udpthroughput .

FROM alpine:3.20
COPY --from=builder /udpthroughput/udpthroughput /usr/local/bin/udpthroughput
EXPOSE 5202/udp
ENTRYPOINT ["/usr/local/bin/udpthroughput"]
