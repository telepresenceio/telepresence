---
title: "Using Telepresence with Docker"
hide_table_of_contents: true
---
# Telepresence with Docker

## Why?

It can be tedious to adopt Telepresence across your organization, since in its handiest form, it requires admin access,
and needs to get along with any exotic networking setup that your company may have.

If Docker is already approved in your organization, this Golden path should be considered.

## How?

When using Telepresence in Docker mode, users can eliminate the need for admin access on their machines, address several networking challenges, and forego the need for third-party applications to enable volume mounts.

You can simply add the docker flag to any Telepresence command, and it will start your daemon in a container.
Thus removing the need for root access, making it easier to adopt as an organization

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

If you want to curl your remote service, you'll need to do that from a container that shares the daemon container's
network. Telepresence provides a `curl` command that will do just that.

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

### Starting the local container prior to the intercept
If you want to start your container manually using `docker run`, you must ensure that it shares the  daemon container's
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

### Use named connections
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

* Using the Docker mode of telepresence **does not require root access**, and makes it **easier** to adopt it across your organization.
* It **limits the potential networking issues** you can encounter.
* It **limits the potential mount issues** you can encounter.
* It **enables simultaneous engagements in multiple namespaces**.
