---
title: Running Telepresence inside a container
hide_table_of_contents: true
---
# Running Telepresence inside a container

## Run with the daemon and engagement handler in containers

The `telepresence connect` command now has the option `--docker`. This option tells telepresence to start the Telepresence daemon in a
docker container.

Running the daemon in a container brings many advantages. The daemon will no longer make modifications to the host's network or DNS, and
it will not mount files in the host's filesystem. Consequently, it will not need admin privileges to run, nor will it need special software
like macFUSE or WinFSP to mount the remote file systems.

The engagement handler (the process that runs locally and optionally will receive intercepted traffic) must also be a docker container,
because that is the only way to access the cluster network that the daemon makes available, and to mount the docker volumes needed.

## Run everything in a container

Environments like [GitHub Codespaces](https://docs.github.com/en/codespaces/overview) runs everything in a container. Your shell, the
telepresence CLI, and both its daemons. This means that the container must be configured so that it allows Telepresence to set up its
Virtual Network Interface before you issue a `telepresence connect`.

There are several conditions that must be met.

- Access to the `/dev/net/tun` device
- The `NET_ADMIN` capability
- If you're using IPv6, then you also need sysctl `net.ipv6.conf.all.disable_ipv6=0`

The Codespaces `devcontainer.json` will typically need to include:

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
