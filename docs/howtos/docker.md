---
title: "Using Telepresence with Docker"
hide_table_of_contents: true
---
# Telepresence with Docker

## Why?

It can be tedious to adopt Telepresence across your organization, since the [package installer](../install/client.md)
requires organizational approval, and Telepresence needs to get along with any exotic networking setup that your
company may have.

If Docker is already approved in your organization, this approach should be considered.

## How?

When using Telepresence in Docker mode, users don't need organizational approval of a package installer, can address several networking challenges, and forego the need for third-party applications to enable volume mounts.

You can simply add the docker flag to any Telepresence command, and it will start your daemon in a container,
making it easier to adopt as an organization.

Let's illustrate with a quick demo, assuming a default Kubernetes context named default, and a simple HTTP service:

```console
$ telepresence connect --docker
Connected to context default, namespace default (https://kubernetes.docker.internal:6443)
```

This method limits the scope of the potential networking issues since everything stays inside Docker. The Telepresence daemon can be found under the name `tp-<your-context>-cn` when listing your containers.

```console
$ docker ps
CONTAINER ID   IMAGE                                        COMMAND                  CREATED          STATUS          PORTS                        NAMES
540a3c12f45b   ghcr.io/telepresenceio/telepresence:2.22.0   "telepresence connec…"   18 seconds ago   Up 16 seconds   127.0.0.1:58802->58802/tcp   tp-default-cn
```

Replace a container in the cluster and start a corresponding local container:

```cli
$ telepresence replace echo-sc --docker-run -- ghcr.io/telepresenceio/echo-server:latest
Using Deployment echo-sc
   Container name: echo-sc
   State         : ACTIVE
   Workload kind : Deployment
   Port forwards : 127.0.0.1 -> 127.0.0.1
       8080 -> 8080 TCP
Echo server listening on port 8080.
```

Using `--docker-run` starts the local container that acts as the handler, so that it uses the same network as the
container that runs the telepresence daemon. It will also receive the same incoming traffic and have the remote volumes
mounted in the same way as the remote container that it replaces.

The network interface that is added when connecting with `telepresence connect --docker` is not accessible directly
from the host computer; it is confined to the daemon container. So if you want to curl your remote service, you'll
need to do that from a container that shares the daemon container's network. Telepresence provides a `curl` command
that will do just that.

```console
$ telepresence curl echo-sc
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100   196  100   196    0     0   4232      0 --:--:-- --:--:-- --:--:--  4260
Request served by 540a3c12f45b

Intercept id b0bd5e75-2618-4bef-ac4e-4c08c4b58ec7:echo-sc/echo-sc
Intercepted container "echo-sc"
HTTP/1.1 GET /

Host: echo-sc
User-Agent: curl/8.11.1
Accept: */*
```

## The --docker-run flag in detail

The `--docker-run` flag makes a `replace`, `ingest`, `intercept`, or `wiretap` use a local handler that runs in a
container. It will establish the attachment, run your container in the foreground, and then automatically end the
attachment when the container exits. It will also ensure that the container shares the network and DNS of the daemon
container.

The flags on the command line are divided into three groups when using `--docker-run`:

- General flags and arguments passed to the telepresence `replace`, `ingest`, `intercept`, or `wiretap`, such as the
  workload name or the port to intercept. The `--docker-run` flag is in itself an example of a general flag.
- Flags and arguments passed to the `docker run` command, such as `--env A=B`.
- Flags and arguments passed to the container that is started.

The syntax of the command is:

```
$ telepresence replace <general flags and args> -- <docker run flags and args> <image> <container flags and args>
```

In essence, everything after the stand-alone double dash `--` is sent to `docker run`.

It's recommended that you always use `--docker-run` in combination with a connection started with
`telepresence connect --docker`, because that makes everything less intrusive:

- No admin user access is needed. Network modifications are confined to a Docker network.
- There's no need for special filesystem mount software like MacFUSE or WinFSP. The volume mounts happen in the Docker engine.

The following happens under the hood when both flags are in use:

- The local container will use a network controlled by the [Teleroute network driver](../reference/plugins.md). This
  guarantees that the handler can access the Telepresence VIF, and hence access the cluster.
- The local container is configured to use DNS provided by the daemon container.
- Volume mounts will be automatic and made using the Telemount Docker volume plugin so that all volumes exposed by the
  targeted remote container are mounted on the local handler container.
- The environment of the remote container becomes the environment of the local handler container.

### Building the container on the fly

The `--docker-build <docker context>` and the repeatable `--docker-build-opt key=value` flags enable containers to be
built on the fly by the replace/ingest/intercept/wiretap command. The `--docker-build` flag implies `--docker-run`.

When using `--docker-build`, the image name used in the argument list must be verbatim `IMAGE`. The word acts as a
placeholder and will be replaced by the ID of the image that is built. The presence of the word `IMAGE` is hence what
separates the flags and arguments sent to `docker run` from the ones sent to the container.

Imagine that the image for an improved version of your frontend service is built by a `Dockerfile` that resides in the
directory `images/frontend-v2`. You can build and replace directly:

```console
$ telepresence replace frontend-v1 --docker-build images/frontend-v2 --docker-build-opt tag=mytag -- IMAGE
```

The `--docker-debug` flag is just like `--docker-build`, but allows a debugger to run inside the container with
relaxed security.

### Automatic flags

Telepresence will automatically pass some relevant flags to Docker to connect the container with the remote container.
Those flags are combined with the arguments given after `--` on the command line.

- `--env-file <file>` Loads the remote environment
- `--name intercept-<intercept name>-<intercept port>` Names the Docker container, this flag is omitted if explicitly given on the command line
- `-v <local mount dir:docker mount dir>` Volume mount specification, see CLI help for `--docker-mount` flags for more info

When used with a container based daemon:
- `--rm` Mandatory, because the volume mounts cannot be removed until the container is removed.
- `-v <telemount volume>:<docker mount dir>` Volume mount specifications propagated from the attached container
- `--network <name of containerized daemon>` Network is shared with the containerized daemon

When used with a daemon that isn't container based:
- `--dns-search tel2-search` Enables single label name lookups in the connected namespace
- `-p <port:container-port>` The local port for the intercept and the container port

### Using --docker-run without a containerized daemon

It is possible to use `--docker-run` with a daemon running on your host, which is the default behavior of Telepresence.

However, it isn't recommended since you'll be in a hybrid mode: while your handler runs in a container, the daemon will
modify the host network, and if remote mounts are desired, they may require extra software.

The ability to use this special combination is retained for backward compatibility reasons. It might be removed in a
future release of Telepresence.

The `--port` flag has slightly different semantics and can be used in situations when the local and container port
must be different. This is done using `--port <local port>:<container port>`. The container port will default to the
local port when using the `--port <port>` syntax.

## Starting the local container prior to the intercept

If you want to start your container manually using `docker run`, you must ensure that it shares the daemon container's
network. A convenient way to do that is to use the `--docker-run` flag as explained above, but you can also start
a container separately using `telepresence docker-run`. This can be done before or after the intercept and the run will
survive cycling the intercept on or off.

```console
$ telepresence docker-run ghcr.io/telepresenceio/echo-server:latest
Echo server listening on port 8080.
```

Check what name the started container has:
```console
$ docker ps --last 1 --format {{.Names}}
fervent_goodall
```

You can now redirect intercepted traffic to your "echo" container using the address flag, e.g.:
```console
$ telepresence intercept --port 8080:80 --address echo fervent_goodall
```

> [!TIP]
> Name your container using the `--name` flag, e.g. `telepresence docker-run --name echo ghcr.io/telepresenceio/echo-server:latest`.
> This will make it easier to refer to it later.

> [!IMPORTANT]
> Never name your container the same as a service in the cluster. If you do, you'll get a warning that the name overrides
> the service IP, and the service will not be reachable.

## Use named connections
You can use the `--name` flag to name the connection if you want to connect to several namespaces simultaneously, e.g.
```console
$ telepresence connect --docker --name alpha --namespace alpha
$ telepresence connect --docker --name beta --namespace beta
```

Now, with two connections active, you must pass the flag `--use <name pattern>` to other commands, e.g.

```console
$ telepresence replace echo-easy --use alpha --docker-run -- ghcr.io/telepresenceio/echo-server:latest
```

## Key learnings

* Using the Docker mode of telepresence **does not require organizational approval of a package installer**, and makes it **easier** to adopt across your organization.
* It **limits the potential networking issues** you can encounter.
* It **limits the potential mount issues** you can encounter.
* It **enables simultaneous attachments in multiple namespaces**.
