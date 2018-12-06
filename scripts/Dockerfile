FROM golang:1.10.1-alpine
RUN apk --no-cache add make iptables sudo git

WORKDIR /root/teleproxy
COPY . .
ENTRYPOINT [ "make" ]
