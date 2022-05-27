FROM golang:alpine3.15 AS builder

WORKDIR /echo-server
COPY go.mod .
COPY go.sum .
# Get dependencies - will also be cached if we won't change mod/sum
RUN go mod download

COPY frontend.go .
COPY main.go .
RUN go build -o echo-server .

RUN ls -l
FROM alpine:3.15
COPY --from=builder /echo-server/echo-server /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/echo-server"]
