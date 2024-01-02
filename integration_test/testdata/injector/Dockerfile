FROM golang:alpine AS builder

WORKDIR /injector
COPY go.mod .
COPY go.sum .
# Get dependencies - will also be cached if we won't change mod/sum
RUN go mod download

COPY main.go .

RUN go build -o injector .

FROM alpine
COPY --from=builder /injector/injector /
CMD ["/injector"]
