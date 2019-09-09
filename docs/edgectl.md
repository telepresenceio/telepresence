# Edge Control

## What is it?

Edge Control is the CLI for a new architecture for Telepresence that is designed to deliver several key things that have been difficult within its current architecture.

We have chosen to introduce a new CLI since the new architecture exposes capabilities that are not easy to fit into the old interface, and we also do not want to destabilize users of the existing Telepresence CLI.

The goals of the new architecture are:

 - Enable a good UX for the "always-on" experience that many Telepresence users have created through custom workarounds.
 - Provide basic functionality to the user with no or minimal cluster-side installation required.
 - Provide advanced functionality without requiring ongoing modification of cluster resources after initial cluster-side installation.
 - Enable a single dev cluster to be safely shared amongst multiple developers.

### Use cases

#### Developing a new service

Jane is a developer who wants to write a new service. The service depends on existing services running in the cluster. Jane can use `edgectl connect` to set up _outbound_ connectivity from her laptop to the cluster. This will allow her work-in-progress implementation of the new service to connect to existing services directly from her laptop.

#### Debugging an existing service

Jane needs to test a bug fix for an existing service running in the cluster. Using `edgectl intercept` Jane can designate a carefully-chosen subset of requests for the service as intercepted. Those requests will get redirected to her laptop, where she can run her modified implementation of the service. All other requests will go to the existing service running in the cluster without disruption.


## Installation

### Laptop

Grab the binary from S3 and install it somewhere in your shell's `PATH`.

For MacOS:

```console
curl -O https://s3.amazonaws.com/datawire-static-files/edgectl/0.7.0/darwin/amd64/edgectl
chmod a+x edgectl
mv edgectl ~/bin  # Somewhere in your PATH
```

For Linux:

```console
curl -O https://s3.amazonaws.com/datawire-static-files/edgectl/0.7.0/linux/amd64/edgectl
chmod a+x edgectl
mv edgectl ~/bin  # Somewhere in your PATH
```

> Note: You can build Edge Control from source, but the straightforward way
>
> ```console
> go get github.com/datawire/teleproxy/cmd/edgectl
> ```
>
> leaves you with a binary that has no embedded version number. If you really want to build from source, check out the repository and run `make build`, which will build binaries in a `bin_*` directory.

Launch the daemon component using `sudo`

```console
sudo edgectl daemon
```

Make sure everything is okay:

```console
$ edgectl version
Client v0.7.0 (api v1)
Daemon v0.7.0 (api v1)

$ edgectl status
Not connected
```

The daemon's logging output may be found in `/tmp/edgectl.log`.

#### Upgrade

Tell the running daemon to exit

```console
$ edgectl quit
Edge Control Daemon quitting...
```

Now you can grab the latest binary and launch the daemon again as above.


### Cluster

Depending on the type of cluster, operations may be involved here. If the cluster is owned by Jane the developer, she will likely set this up herself. If the cluster is shared, it may be the case that Jane does not have permission to perform these actions and the cluster owner will need to handle them.

#### Traffic Manager

[...]

#### Traffic Agent

[...]


## Usage

### Outbound

The cluster has useful services that we want to access while developing or debugging our application:

```console
$ kubectl create deploy hello-world --image=ark3/hello-world
deployment.apps/hello-world created

$ kubectl expose deploy hello-world --port 80 --target-port 8000
service/hello-world exposed

$ kubectl get svc,deploy hello-world
NAME                  TYPE        CLUSTER-IP    EXTERNAL-IP   PORT(S)   AGE
service/hello-world   ClusterIP   10.43.26.64   <none>        80/TCP    5m21s

NAME                                READY   UP-TO-DATE   AVAILABLE   AGE
deployment.extensions/hello-world   1/1     1            1           5m43s
```

Use Edge Control to set up outbound connectivity to your cluster.

```console
$ edgectl status
Not connected

$ edgectl connect
Connecting...
Connected to context default (https://localhost:6443)

Unable to connect to the traffic manager in your cluster.
The intercept feature will not be available.
Error was: kubectl get svc/deploy telepresency-proxy: exit status 1

$ edgectl status
Connected
  Context:       default (https://localhost:6443)
  Proxy:         ON (networking to the cluster is enabled)
  Intercepts:    Unavailable: no traffic manager

$ curl hello-world
Hello, world!
```

When you're done working with this cluster, disconnect.

```console
$ edgectl disconnect
Disconnected

$ edgectl status
Not connected
```

### Intercept

> This doesn't include setup... FIXME

Make sure you have the echo server running locally on your laptop.

```console
$ docker run --rm -d -h container_on_laptop -p 8080:8080 jmalloc/echo-server
8ff1d90d1b6bbdbb845fbfe1f76c5abc325de591cb154bb5e3cc0c198730339d

$ curl localhost:8080/foo/bar
Request served by container_on_laptop

HTTP/1.1 GET /foo/bar

Host: localhost:8080
User-Agent: curl/7.65.3
Accept: */*

```

Connect to the cluster to set up outbound connectivity and check that you can access the echo service in the cluster with `curl`.

```console

$ edgectl status
Connected
  Context:       admin@kubernetes (https://54.159.169.153:6443)
  Proxy:         ON (networking to the cluster is enabled)
  Interceptable: 0 deployments
  Intercepts:    ? total, 0 local

$ edgectl intercept avail
Found 3 interceptable deployment(s):
   1. intercepted
   2. model-cluster-app
   3. echo

$ curl echo/foo/bar
Request served by echo-97f77648d-jnn2x

HTTP/1.1 GET /foo/bar

Host: echo
User-Agent: curl/7.65.3
Accept: */*
X-Forwarded-Proto: http
X-Request-Id: 5cae74f6-5709-4e89-945a-d458fbc08007
X-Envoy-Expected-Rq-Timeout-Ms: 15000
Content-Length: 0

$ curl echo/ark3/foo/bar
Request served by echo-97f77648d-jhlf5

HTTP/1.1 GET /ark3/foo/bar

Host: echo
X-Request-Id: 0638287f-fde5-48e2-9035-3a569058e1fa
X-Envoy-Expected-Rq-Timeout-Ms: 15000
Content-Length: 0
User-Agent: curl/7.65.3
Accept: */*
X-Forwarded-Proto: http

```

Set up an intercept. In this example, we'll capture requests that include "ark3" in the request path.

```console
$ edgectl intercept list
No intercepts

$ edgectl intercept add echo -m :path=.*ark3.* -t localhost:8080 -n test1
Added intercept "test1"

$ edgectl intercept list
   1. test1
      Intercepting requests to echo when
      - :path: .*ark3.*
      and redirecting them to localhost:8080

$ curl echo/foo/bar
Request served by echo-97f77648d-jnn2x

HTTP/1.1 GET /foo/bar

Host: echo
Content-Length: 0
User-Agent: curl/7.65.3
Accept: */*
X-Forwarded-Proto: http
X-Request-Id: ab1092fc-92eb-4ee3-a1f3-e12ecb7841a3
X-Envoy-Expected-Rq-Timeout-Ms: 15000

$ curl echo/ark3/foo/bar
Request served by container_on_laptop

HTTP/1.1 GET /ark3/foo/bar

Host: echo
X-Request-Id: e4800f83-9405-43d0-9e7d-b3d583214491
X-Envoy-Expected-Rq-Timeout-Ms: 15000
Content-Length: 0
User-Agent: curl/7.65.3
Accept: */*
X-Forwarded-Proto: http

```

As you can see, the second request, which includes "ark3" in the path, is served by the container running on my laptop.

Next, remove the intercept to restore normal operation.

```console
$ edgectl intercept remove test1
Removed intercept "test1"

$ curl echo/ark3/foo/bar
Request served by echo-97f77648d-jhlf5

HTTP/1.1 GET /ark3/foo/bar

Host: echo
X-Forwarded-Proto: http
X-Request-Id: 1ef5fee4-3e06-4b60-8d53-4764d07e9709
X-Envoy-Expected-Rq-Timeout-Ms: 15000
Content-Length: 0
User-Agent: curl/7.65.3
Accept: */*

```

Requests are no longer intercepted.

Multiple intercepts of the same deployment can run at the same time too. You can direct them to the same machine, allowing you to "or" together intercept conditions. Also, multiple developers can intercept the same deployment simultaneously. As long as their match patterns don't collide, they don't need to worry about disrupting one another.

Here's another example using a header match. This could easily be a browser cookie or a particular authorization header.

```console
$ edgectl intercept add echo -m x-service-preview=dev -t localhost:8080 -n test2
Added intercept "test2"

$ curl -H "x-service-preview: dev" echo
Request served by container_on_laptop

HTTP/1.1 GET /

Host: echo
X-Service-Preview: dev
X-Envoy-Expected-Rq-Timeout-Ms: 15000
Content-Length: 0
User-Agent: curl/7.65.3
Accept: */*
X-Forwarded-Proto: http
X-Request-Id: e44d0296-677b-48d6-a8f4-6edf5ca15867

$ curl -H "x-service-preview: prod" echo
Request served by echo-97f77648d-jhlf5

HTTP/1.1 GET /

Host: echo
User-Agent: curl/7.65.3
Accept: */*
X-Service-Preview: prod
X-Forwarded-Proto: http
X-Request-Id: bfd97032-e9cf-48ef-9a9d-7c1f98a5b0f5
X-Envoy-Expected-Rq-Timeout-Ms: 15000
Content-Length: 0

$ edgectl intercept add echo -m x-allow-intercept=.* -m x-user=ark3 -t localhost:8080 -n test3
Added intercept "test3"

$ curl -H "x-allow-intercept: true" -H "x-user: bob" echo
Request served by echo-97f77648d-gqwtz

HTTP/1.1 GET /

Host: echo
Content-Length: 0
X-Allow-Intercept: true
X-User: bob
X-Forwarded-Proto: http
X-Request-Id: b907804e-a700-4f74-96e5-814d993c5003
User-Agent: curl/7.65.3
Accept: */*
X-Envoy-Expected-Rq-Timeout-Ms: 15000

$ curl -H "x-allow-intercept: true" -H "x-user: ark3" echo
Request served by container_on_laptop

HTTP/1.1 GET /

Host: echo
User-Agent: curl/7.65.3
Accept: */*
X-Allow-Intercept: true
X-User: ark3
X-Forwarded-Proto: http
X-Envoy-Expected-Rq-Timeout-Ms: 15000
Content-Length: 0
X-Request-Id: cbaa6aa4-a623-4636-bf9b-d5a48e94d6fd

$ edgectl intercept list
   1. test2
      Intercepting requests to echo when
      - x-service-preview: dev
      and redirecting them to localhost:8080
   2. test3
      Intercepting requests to echo when
      - x-allow-intercept: .*
      - x-user: ark3
      and redirecting them to localhost:8080
```
