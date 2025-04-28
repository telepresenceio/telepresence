---
title: Using Docker for engagements
description: How a Telepresence engagement can run a Docker container with configured environment and volume mounts.
toc_min_heading_level: 2
toc_max_heading_level: 2
---

# Using Docker when engaging with workloads

## Using command flags

### The docker flag
You can start the Telepresence daemon in a Docker container on your laptop using the command:

```console
$ telepresence connect --docker
```

### The telepresence curl command

The network interface that is added when connecting using `telepresence connect --docker` will not be accessible
directly from the host computer. It is confined to the telepresence daemon container.

You can use the `telepresence curl` command to curl your cluster resources. It will run curl in a docker container that
shares the network and DNS of the daemon container.

### The telepresence docker-run command

You can use the `telepresence docker-run` command to start a container that shares the network and DNS of the daemon
container.

### The replace/ingest/intercept/wiretap --docker-run flag

You can use the `--docker-run` flag if you want your `replace`, `ingest`, `intercept`, or `wiretap` to use a local
handler that runs in a container. It will establish the engagement, run your container in the foreground, and then
automatically end the engagement when the container exits. It will also ensure that the container shares the network
and DNS of the daemon container.

Please note that there your flags are divided into three groups when using `--docker-run`

- General flags and arguments passed to the telepresence `replace`, `ingest`, `intercept`, or `wiretap` such as the
  workload name or the port to intercept. The `--docker-run` flag is in itself an example of a general flag.
- Flags and arguments passed to the `docker run` command such as `--env A=B`.
- Flags and arguments passed to the container that is started.

The syntax of the command is
```
$ telepresence replace <general flags and args> -- <docker run flags and args> <image> <container flags and args>
```

In essence, everything after the stand-alone double dash `--` is sent to the `docker run`.

The `--` separates flags intended for `telepresence replace/ingest/intercept/wiretap` from flags intended for `docker run`.

It's recommended that you always use the `--docker-run` in combination with a connection started with the
`telepresence connect --docker`, because that makes everything less intrusive:

- No admin user access is needed. Network modifications are confined to a Docker network.
- There's no need for special filesystem mount software like MacFUSE or WinFSP. The volume mounts happen in the Docker engine.

The following happens under the hood when both flags are in use:

- The local container will use a network controlled by the Teleroute network driver. This guarantees that the handler
  can access the Telepresence VIF, and hence access the cluster.
- The local container is configured to use DNS provided by the daemon container.
- Volume mounts will be automatic and made using the Telemount Docker volume plugin so that all volumes exposed by the
  targeted remote container are mounted on the local handler container.
- The environment of the remote container becomes the environment of the local handler container.

### The docker-build flag

The `--docker-build <docker context>` and the repeatable `docker-build-opt key=value` flags enable containers to be
built on the fly by the replace/ingest/intercept/wiretap command.

When using `--docker-build`, the image name used in the argument list must be verbatim `IMAGE`. The word acts as a
placeholder and will be replaced by the ID of the image that is built. The presence of the word `IMAGE` is hence what
separates the flags and arguments sent to `docker run` from the ones sent to the container.

The `--docker-build` flag implies `--docker-run`.

### The docker-debug flag

This flag is just like --docker-build, but allows a debugger to run inside the container with relaxed security.

## Using docker-run flag without docker

It is possible to use `--docker-run` with a daemon running on your host, which is the default behavior of Telepresence. 

However, it isn't recommended since you'll be in a hybrid mode: while your handler runs in a container, the daemon will modify the host network, and if remote mounts are desired, they may require extra software. 

The ability to use this special combination is retained for backward compatibility reasons. It might be removed in a future release of Telepresence.

The `--port` flag has slightly different semantics and can be used in situations when the local and container port must be different. This
is done using `--port <local port>:<container port>`. The container port will default to the local port when using the `--port <port>` syntax.

## Examples

Imagine you are working on a new version of your frontend service.  It is running in your cluster as a Deployment called `frontend-v1`. You use Docker on your laptop to build an improved version of the container called `frontend-v2`.  To test it out, use this command to run the new container on your laptop and start an intercept of the cluster service to your local container.

```console
$ telepresence connect --docker
$ telepresence replace frontend-v1 --docker-run -- frontend-v2
```

Now, imagine that the `frontend-v2` image is built by a `Dockerfile` that resides in the directory `images/frontend-v2`. You can build and replace directly.

```console
$ telepresence replace frontend-v1 --docker-build images/frontend-v2 --docker-build-opt tag=mytag -- IMAGE
```

## Automatic flags

Telepresence will automatically pass some relevant flags to Docker to connect the container with the remote container. Those flags are combined with the arguments given after `--` on the command line.

- `--env-file <file>` Loads the remote environment
- `--name intercept-<intercept name>-<intercept port>` Names the Docker container, this flag is omitted if explicitly given on the command line
- `-v <local mount dir:docker mount dir>` Volume mount specification, see CLI help for `--docker-mount` flags for more info

When used with a container based daemon:
- `--rm` Mandatory, because the volume mounts cannot be removed until the container is removed.
- `-v <telemount volume>:<docker mount dir>` Volume mount specifications propagated from the engaged container
- `--network <name of containerized daemon>` Network is shared with the containerized daemon

When used with a daemon that isn't container based:
- `--dns-search tel2-search` Enables single label name lookups in the connected namespace
- `-p <port:container-port>` The local port for the intercept and the container port
