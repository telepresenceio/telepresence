FROM golang:alpine AS base

# Build Delve
RUN go install github.com/go-delve/delve/cmd/dlv@latest

WORKDIR /echo-server
COPY go.mod .
COPY go.sum .
# Get dependencies - will also be cached if we won't change mod/sum
RUN go mod download

COPY frontend.go .
COPY main.go .

FROM base as builder
RUN go build -o echo-server .

FROM base as development
RUN go build -gcflags="all=-N -l" -o echo-server .
EXPOSE 40000
CMD ["/go/bin/dlv", "--listen=:40000", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/echo-server/echo-server"]

FROM alpine as production
COPY --from=builder /echo-server/echo-server /
CMD ["/echo-server"]
