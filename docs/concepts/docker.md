# Telepresence with Docker Golden Path

## Why?

It can be tedious to adopt Telepresence across your organization, since in its handiest form, it requires admin access, and needs to get along with any exotic
networking setup that your company may have.

If Docker is already approved in your organization, this Golden path should be considered.

## How?

When using Telepresence in Docker mode, users can eliminate the need for admin access on their machines, address several networking challenges, and forego the need for third-party applications to enable volume mounts.

You can simply add the docker flag to any Telepresence command, and it will start your daemon in a container.
Thus removing the need for root access, making it easier to adopt as an organization

Let's illustrate with a quick demo, assuming a default Kubernetes context named default, and a simple HTTP service:

```cli
$ telepresence connect --docker
Connected to context default (https://default.cluster.bakerstreet.io)

$ docker ps
CONTAINER ID   IMAGE                          COMMAND                  CREATED          STATUS          PORTS                        NAMES
7a0e01cab325   datawire/telepresence:2.12.1   "telepresence connec…"   18 seconds ago   Up 16 seconds   127.0.0.1:58802->58802/tcp   tp-default
```

This method limits the scope of the potential networking issues since everything stays inside Docker. The Telepresence daemon can be found under the name `tp-<your-context>` when listing your containers.

Start an intercept and a corresponding intercept-handler:

```cli
$ telepresence intercept echo-easy --port 8080:80 --docker-run -- jmalloc/echo-server
Using Deployment echo-easy
   Intercept name         : echo-easy
   State                  : ACTIVE
   Workload kind          : Deployment
   Destination            : 127.0.0.1:8080
   Service Port Identifier: proxied
   Intercepting           : all TCP requests
Echo server listening on port 8080.
```

Using `--docker-run` starts the local container that acts as the intercept handler so that it uses the same network as the container that runs the telepresence daemon. It will also
have the remote volumes mounted in the same way as the remote container that it intercepts.

If you want to curl your remote service, you'll need to do that from a container that shares the daemon container's network. You can find the network using `telepresence status`:
```cli
$ telepresence status | grep 'Container network'
  Container network : container:tp-default-default-cn
```

Now curl with a `docker run` that uses that network:
```cli
$ docker run --network container:tp-default-default-cn --rm curlimages/curl echo-easy
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100    99  100    99    0     0  21104      0 --:--:-- --:--:-- -Request served by 4b225bc8d6f1

GET / HTTP/1.1

Host: echo-easy
Accept: */*
User-Agent: curl/8.6.0
-:--:-- 24750
```

Similarly, if you want to start your intercept handler manually using `docker run`, you must ensure that it shares the daemon container's network:

```cli
$ docker run \
  --network=container:tp-default \
  -e PORT=8080 jmalloc/echo-server
Echo server listening on port 8080.
```

### Tip. Use named connections
You can use the `--name` flag to name the connection and get a shorter network name:

```
$ telepresence quit
$ telepresence connect --docker --name a
```
Now, the network name will be `tp-a` instead of `tp-default-default-cn`.

Naming is also very useful when you want to connect to several namespaces simultaneously, e.g.

```
$ telepresence connect --docker --name alpha --namespace alpha
$ telepresence connect --docker --name beta --namespace beta
```

Now, with two connections active, you must pass the flag `--use <name pattern>` to other commands, e.g.
```
$ telepresence intercept echo-easy --use alpha --port 8080:80 --docker-run -- jmalloc/echo-server
```

## Key learnings

* Using the Docker mode of telepresence **does not require root access**, and makes it **easier** to adopt it across your organization.
* It **limits the potential networking issues** you can encounter.
* It **limits the potential mount issues** you can encounter.
* It **enables simultaneous intercepts in multiple namespaces**.
* It leverages **Docker** for your interceptor.
