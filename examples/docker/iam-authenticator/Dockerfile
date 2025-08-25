FROM golang:alpine AS auth-builder
RUN go install sigs.k8s.io/aws-iam-authenticator/cmd/aws-iam-authenticator@latest

# Dockerfile with telepresence and its prerequisites
FROM alpine

# Install Telepresence prerequisites
RUN apk add --no-cache curl iproute2 sshfs

# Download and install the telepresence binary
RUN curl -fL https://github.com/telepresenceio/telepresence/releases/download/v2.24.0-rc.0/telepresence-linux-amd64 -o telepresence && \
   install -o root -g root -m 0755 telepresence /usr/local/bin/telepresence

COPY --from=auth-builder /go/bin/aws-iam-authenticator ./aws-iam-authenticator
RUN install -o root -g root -m 0755 aws-iam-authenticator /usr/local/bin/aws-iam-authenticator && \
    rm aws-iam-authenticator
