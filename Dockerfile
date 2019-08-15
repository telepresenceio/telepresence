FROM golang:1.12-alpine as builder
WORKDIR /app

COPY . .

RUN go install -mod=vendor ./cmd/httptest

RUN ls /go/bin

FROM alpine:3.10.1
COPY --from=builder /go/bin/httptest /usr/local/bin/httptest
ENTRYPOINT ["/usr/local/bin/httptest"]
