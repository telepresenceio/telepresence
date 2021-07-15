# Running Telepresence inside a container

It is sometimes desirable to run Telepresence inside a container. One reason can be to avoid any side effects on the workstation's network, another can be to establish multiple sessions with the traffic manager, or even work with different clusters simultaneously.

## Building the container

Building a container with a ready-to-run Telepresence is easy because there are relatively few external dependencies. Add the following to a `Dockerfile`:

```Dockerfile
# Dockerfile with telepresence and its prerequisites
FROM alpine:3.13

# Install Telepresence prerequisites
RUN apk add --no-cache curl iproute2 sshfs

# Download and install the telepresence binary
RUN curl -fL https://app.getambassador.io/download/tel2/linux/amd64/latest/telepresence -o telepresence && \
   install -o root -g root -m 0755 telepresence /usr/local/bin/telepresence
```
In order to build the container, do this in the same directory as the `Dockerfile`:
```
$ docker build -t tp-in-docker .
```

## Running the container

Telepresence will need access to the `/dev/net/tun` device on your Linux host (or, in case the host isn't Linux, the Linux VM that Docker starts automatically), and a Kubernetes config that identifies the cluster. It will also need `--cap-add=NET_ADMIN` to create its Virtual Network Interface.

The command to run the container can look like this:
```bash
$ docker run \
  --cap-add=NET_ADMIN \
  --device /dev/net/tun:/dev/net/tun \
  --network=host \
  -v ~/.kube/config:/root/.kube/config \
  -it --rm tp-in-docker
```
