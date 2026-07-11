---
title: Running the Telepresence client in a container
description: Run the entire Telepresence client — CLI, daemons, and attachment handlers — inside a single container, for environments like GitHub Codespaces or CI/CD pipelines.
hide_table_of_contents: true
---
# Running the Telepresence client in a container

This page covers a corner case: running the *entire* Telepresence client —
the CLI, both daemons, and any attachment handler — inside one container.
You need this when the container **is** your workstation, as in a
[GitHub Codespaces](https://docs.github.com/en/codespaces/overview)
devcontainer, or when Telepresence runs unattended in a CI/CD pipeline.
When you use Telepresence on an ordinary workstation, you don't.

> [!IMPORTANT]
> If you are looking for how to combine Telepresence with Docker on your
> workstation — running the daemon and your containerized services in
> containers while the CLI stays on the host — that is the far more common
> `telepresence connect --docker` mode, described in
> [Use Telepresence with Docker](docker.md). It needs none of the special
> privileges described here.

## Container requirements

Telepresence's root daemon sets up a Virtual Network Interface, and when
everything runs inside one container, that happens *inside the container*.
The container must therefore be started with:

- Access to the `/dev/net/tun` device
- The `NET_ADMIN` capability
- If you're using IPv6, the sysctl `net.ipv6.conf.all.disable_ipv6=0`

A Codespaces `devcontainer.json` will typically need to include:

```json
    "runArgs": [
        "--privileged",
        "--cap-add=NET_ADMIN",
    ],
```

## Kubernetes auth plugins

If Kubernetes auth plugins are needed, they must be installed into the same container as Telepresence. Each auth plugin
will need a different approach.

### AWS IAM Authenticator

1. Install the AWS IAM Authenticator Go binary.

```dockerfile
FROM golang:alpine AS auth-builder
RUN go install sigs.k8s.io/aws-iam-authenticator/cmd/aws-iam-authenticator@latest

# Dockerfile with telepresence and its prerequisites
FROM alpine

# Install Telepresence prerequisites
RUN apk add --no-cache curl iproute2 sshfs

# Download and install the telepresence binary
RUN curl -fL https://github.com/telepresenceio/telepresence/releases/download/v$version$/telepresence-linux-amd64 -o telepresence && \
   install -o root -g root -m 0755 telepresence /usr/local/bin/telepresence

COPY --from=auth-builder /go/bin/aws-iam-authenticator ./aws-iam-authenticator
RUN install -o root -g root -m 0755 aws-iam-authenticator /usr/local/bin/aws-iam-authenticator && \
    rm aws-iam-authenticator
```

2. Ensure that the authenticator can reach your kubconfig and AWS configuration by mounting them into the container:

```console
$ docker run \
  --cap-add=NET_ADMIN \
  --device /dev/net/tun:/dev/net/tun \
  --network=host \
  -v ~/.kube/config:/root/.kube/config \
  -v ~/.aws:/root/.aws \
  -it --rm tp-in-docker
```
