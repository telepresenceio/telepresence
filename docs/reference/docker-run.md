---
Description: "How a Telepresence intercept can run a Docker container with configured environment and volume mounts."
---

# Using Docker for intercepts

## Using command flags

### The docker flag
You can start the Telepresence daemon in a Docker container on your laptop using the command:

```console
$ telepresence connect --docker
```

The `--docker` flag is a global flag, and if passed directly like `telepresence intercept --docker`, then the implicit connect that takes place if no connections is active, will use a container based daemon.

### The docker-run flag

If you want your intercept to go to another Docker container, you can use the `--docker-run` flag. It creates the intercept, runs your container in the foreground, then automatically ends the intercept when the container exits.

```console
$ telepresence intercept <service_name> --port <port> --docker-run -- <docker run arguments> <image> <container arguments>
```

The `--` separates flags intended for `telepresence intercept` from flags intended for `docker run`.

It's recommended that you always use the `--docker-run` in combination with the global `--docker` flag, because that makes everything less intrusive.
- No admin user access is needed. Network modifications are confined to a Docker network.
- There's no need for special filesystem mount software like MacFUSE or WinFSP. The volume mounts happen in the Docker engine.

The following happens under the hood when both flags are in use:

- The network of for the intercept handler will be set to the same as the network used by the daemon. This guarantees that the
  intercept handler can access the Telepresence VIF, and hence have access the cluster.
- Volume mounts will be automatic and made using the Telemount Docker volume plugin so that all volumes exposed by the intercepted
  container are mounted on the intercept handler container.
- The environment of the intercepted container becomes the environment of the intercept handler container.

### The docker-build flag

The `--docker-build <docker context>` and the repeatable `docker-build-opt key=value` flags enable container's to be build on the fly by the intercept command.

When using `--docker-build`, the image name used in the argument list must be verbatim `IMAGE`. The word acts as a placeholder and will be replaced by the ID of the image that is built.

The `--docker-build` flag implies `--docker-run`.

## Using docker-run flag without docker

It is possible to use `--docker-run` with a daemon running on your host, which is the default behavior of Telepresence. 

However, it isn't recommended since you'll be in a hybrid mode: while your intercept runs in a container, the daemon will modify the host network, and if remote mounts are desired, they may require extra software. 

The ability to use this special combination is retained for backward compatibility reasons. It might be removed in a future release of Telepresence.

The `--port` flag has slightly different semantics and can be used in situations when the local and container port must be different. This
is done using `--port <local port>:<container port>`. The container port will default to the local port when using the `--port <port>` syntax.

## Examples

Imagine you are working on a new version of your frontend service.  It is running in your cluster as a Deployment called `frontend-v1`. You use Docker on your laptop to build an improved version of the container called `frontend-v2`.  To test it out, use this command to run the new container on your laptop and start an intercept of the cluster service to your local container.

```console
$ telepresence intercept --docker frontend-v1 --port 8000 --docker-run -- frontend-v2
```

Now, imagine that the `frontend-v2` image is built by a `Dockerfile` that resides in the directory `images/frontend-v2`. You can build and intercept directly.

```console
$ telepresence intercept --docker frontend-v1 --port8000 --docker-build images/frontend-v2 --docker-build-opt tag=mytag -- IMAGE
```

## Automatic flags

Telepresence will automatically pass some relevant flags to Docker in order to connect the container with the intercept. Those flags are combined with the arguments given after `--` on the command line.

- `--env-file <file>` Loads the intercepted environment
- `--name intercept-<intercept name>-<intercept port>` Names the Docker container, this flag is omitted if explicitly given on the command line
- `-v <local mount dir:docker mount dir>` Volume mount specification, see CLI help for `--docker-mount` flags for more info

When used with a container based daemon:
- `--rm` Mandatory, because the volume mounts cannot be removed until the container is removed.
- `-v <telemount volume>:<docker mount dir>` Volume mount specifications propagated from the intercepted container

When used with a daemon that isn't container based:
- `--dns-search tel2-search` Enables single label name lookups in intercepted namespaces
- `-p <port:container-port>` The local port for the intercept and the container port
