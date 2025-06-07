FROM --platform=$BUILDPLATFORM golang:alpine AS builder

RUN apk add --no-cache --virtual .build-deps gcc libc-dev
WORKDIR /docker-network-teleroute
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /go/bin/docker-network-teleroute

FROM alpine
RUN apk add --no-cache bash
COPY --from=builder /go/bin/docker-network-teleroute /bin/
