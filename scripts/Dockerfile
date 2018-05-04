FROM golang:1.10.1-alpine
RUN apk --no-cache add make iptables sudo git

RUN go get github.com/google/shlex \
           github.com/miekg/dns \
           golang.org/x/net/proxy \
	   k8s.io/api/core/v1 \
	   k8s.io/apimachinery/pkg/fields \
	   k8s.io/client-go/kubernetes \
	   k8s.io/client-go/plugin/pkg/client/auth \
	   k8s.io/client-go/tools/clientcmd \
           k8s.io/client-go/tools/cache

WORKDIR /go/src/github.com/datawire/teleproxy

COPY . .
ENTRYPOINT [ "make" ]
