FROM golang:1.25-alpine AS builder

WORKDIR /h3server
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o h3server .

FROM alpine:3.20
COPY --from=builder /h3server/h3server /usr/local/bin/h3server
EXPOSE 443/udp
ENTRYPOINT ["/usr/local/bin/h3server"]
