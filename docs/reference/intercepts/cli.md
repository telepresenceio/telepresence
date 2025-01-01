---
title: Configure intercept using CLI
---

# Configuring intercept using CLI

## Specifying a namespace for an intercept

The namespace of the intercepted workload is specified during connect using the `--namespace` option.

```shell
telepresence connect --namespace myns
telepresence intercept hello --port 9000
```

## Importing environment variables

Telepresence can import the environment variables from the pod that is
being intercepted, see [this doc](../environment.md) for more details.

## Creating an intercept

The following command will intercept all traffic bound to the service and proxy it to your
laptop. This includes traffic coming through your ingress controller,  so use this option
carefully as to not disrupt production environments.

```shell
telepresence intercept <deployment name> --port=<TCP port>
```

Run `telepresence status` to see the list of active intercepts.

```console
$ telepresence status
OSS User Daemon: Running
  Version           : v2.18.0
  Executable        : /usr/local/bin/telepresence
  Install ID        : 4b1658f3-7ff8-4af3-66693-f521bc1da32f
  Status            : Connected
  Kubernetes server : https://cluster public IP>
  Kubernetes context: default
  Namespace         : default
  Manager namespace : ambassador
  Intercepts        : 1 total
    dataprocessingnodeservice: <laptop username>@<laptop name>
OSS Root Daemon: Running
  Version: v2.18.0
  DNS    : 
    Remote IP       : 127.0.0.1
    Exclude suffixes: [.com .io .net .org .ru]
    Include suffixes: []
    Timeout         : 8s
  Subnets: (2 subnets)
    - 10.96.0.0/16
    - 10.244.0.0/24
OSS Traffic Manager: Connected
  Version      : v2.19.0
  Traffic Agent: docker.io/datawire/tel2:2.18.0
```

Finally, run `telepresence leave <name of intercept>` to stop the intercept.

[kube-multi-port-services]: https://kubernetes.io/docs/concepts/services-networking/service/#multi-port-services

```console
$ telepresence intercept <base name of intercept> --port=<local TCP port>:<servicePortIdentifier>
Using Deployment <name of deployment>
intercepted
    Intercept name         : <full name of intercept>
    State                  : ACTIVE
    Workload kind          : Deployment
    Destination            : 127.0.0.1:<local TCP port>
    Service Port Identifier: <servicePortIdentifier>
    Intercepting           : all TCP connections
```

When intercepting a service that has multiple ports, the name of the
service port that has been intercepted is also listed.

If you want to change which port has been intercepted, you can create
a new intercept the same way you did above, and it will change which
service port is being intercepted.

## Creating an intercept when multiple services match your workload

Oftentimes, there's a 1-to-1 relationship between a service and a
workload, so telepresence is able to auto-detect which service it
should intercept based on the workload you are trying to intercept.
But if you use something like
[Argo](https://www.getambassador.io/docs/argo/latest/), there may be
two services (that use the same labels) to manage traffic between a
canary and a stable service.

Fortunately, if you know which service you want to use when
intercepting a workload, you can use the `--service` flag.  So in the
aforementioned example, if you wanted to use the `echo-stable` service
when intercepting your workload, your command would look like this:

```console
$ telepresence intercept echo-rollout-<generatedHash> --port <local TCP port> --service echo-stable
Using ReplicaSet echo-rollout-<generatedHash>
intercepted
    Intercept name    : echo-rollout-<generatedHash>
    State             : ACTIVE
    Workload kind     : ReplicaSet
    Destination       : 127.0.0.1:3000
    Volume Mount Point: /var/folders/cp/2r22shfd50d9ymgrw14fd23r0000gp/T/telfs-921196036
    Intercepting      : all TCP connections
```

## Intercepting multiple ports

It is possible to intercept more than one service and/or service port that are using the same workload. You do this
by repeating the `--port` flag.

Let's assume that we have a service `multi-echo` with the two ports `http` and `grpc`. They are both
targeting the same `multi-echo` deployment.

```console
$ telepresence intercept multi-echo-http --workload multi-echo --port 8080:http --port 8443:grpc
Using Deployment multi-echo
intercepted
    Intercept name         : multi-echo-http
    State                  : ACTIVE
    Workload kind          : Deployment
    Intercepting           : 10.1.54.120 -> 127.0.0.1
        8080 -> 8080 TCP
        8443 -> 8443 TCP
    Volume Mount Point     : /tmp/telfs-893700837
```

## Port-forwarding an intercepted container's sidecars

Sidecars are containers that sit in the same pod as an application
container; they usually provide auxiliary functionality to an
application, and can usually be reached at
`localhost:${SIDECAR_PORT}`.  For example, a common use case for a
sidecar is to proxy requests to a database, your application would
connect to `localhost:${SIDECAR_PORT}`, and the sidecar would then
connect to the database, perhaps augmenting the connection with TLS or
authentication.

When intercepting a container that uses sidecars, you might want those
sidecars' ports to be available to your local application at
`localhost:${SIDECAR_PORT}`, exactly as they would be if running
in-cluster.  Telepresence's `--to-pod ${PORT}` flag implements this
behavior, adding port-forwards for the port given.

```console
$ telepresence intercept <base name of intercept> --port=<local TCP port>:<servicePortIdentifier> --to-pod=<sidecarPort>
Using Deployment <name of deployment>
intercepted
    Intercept name         : <full name of intercept>
    State                  : ACTIVE
    Workload kind          : Deployment
    Destination            : 127.0.0.1:<local TCP port>
    Service Port Identifier: <servicePortIdentifier>
    Intercepting           : all TCP connections
```

If there are multiple ports that you need forwarded, simply repeat the
flag (`--to-pod=<sidecarPort0> --to-pod=<sidecarPort1>`).

## Intercepting headless services

Kubernetes supports creating [services without a ClusterIP](https://kubernetes.io/docs/concepts/services-networking/service/#headless-services),
which, when they have a pod selector, serve to provide a DNS record that will directly point to the service's backing pods.
Telepresence supports intercepting these `headless` services as it would a regular service with a ClusterIP.
So, for example, if you have the following service:

```yaml
---
apiVersion: v1
kind: Service
metadata:
  name: my-headless
spec:
  type: ClusterIP
  clusterIP: None
  selector:
    service: my-headless
  ports:
    - port: 8080
      targetPort: 8080
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-headless
  labels:
    service: my-headless
spec:
  replicas: 1
  serviceName: my-headless
  selector:
    matchLabels:
      service: my-headless
  template:
    metadata:
      labels:
        service: my-headless
    spec:
      containers:
        - name: my-headless
          image: jmalloc/echo-server
          ports:
            - containerPort: 8080
          resources: {}
```

You can intercept it like any other:

```console
$ telepresence intercept my-headless --port 8080
Using StatefulSet my-headless
intercepted
    Intercept name    : my-headless
    State             : ACTIVE
    Workload kind     : StatefulSet
    Destination       : 127.0.0.1:8080
    Volume Mount Point: /var/folders/j8/kzkn41mx2wsd_ny9hrgd66fc0000gp/T/telfs-524189712
    Intercepting      : all TCP connections
```

> [!IMPORTANT]
> This utilizes an `initContainer` that requires `NET_ADMIN` capabilities.
> If your cluster administrator has disabled them, you will be unable to use numeric ports with the agent injector.

## Intercepting without a service

You can intercept a workload without a service by adding an annotation that informs Telepresence what container
ports that are eligable for intercepts. Telepresence will then inject a traffic-agent when the workload is
deployed, and you will be able to intercept the given ports as if they were service ports. The annotation is:

```yaml
      annotations:
        telepresence.getambassador.io/inject-container-ports: http
```

The annotation value is a comma separated list of port identifiers consisting of either the name or the port number of a container
port, optionally suffixed with `/TCP` or `/UDP`

### Let's try it out!

1. Deploy an annotation similar to this one to your cluster:

   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: echo-no-svc
     labels:
       app: echo-no-svc
   spec:
     replicas: 1
     selector:
       matchLabels:
         app: echo-no-svc
     template:
       metadata:
         labels:
           app: echo-no-svc
         annotations:
           telepresence.getambassador.io/inject-container-ports: http
       spec:
         automountServiceAccountToken: false
         containers:
           - name: echo-server
             image: ghcr.io/telepresenceio/echo-server:latest
             ports:
               - name: http
                 containerPort: 8080
             env:
               - name: PORT
                 value: "8080"
             resources:
               limits:
                 cpu: 50m
                 memory: 8Mi
   ```

2. Connect telepresence:

    ```console
    $ telepresence connect
    Launching Telepresence User Daemon
    Launching Telepresence Root Daemon
    Connected to context kind-dev, namespace default (https://127.0.0.1:36767)
    ```

3. List your intercept eligible workloads. If the annotation is correct, the deployment should show up in the list:

   ```console
   $ telepresence list
   echo-no-svc: ready to intercept (traffic-agent not yet installed)
   ```

4. Start an intercept handler locally that will receive the incoming traffic. Here's an example using a simple python http service:

   ```console
   $ python3 -m http.server 8080
   ```

5. Create an intercept:

   ```console
   $ telepresence intercept echo-no-svc
   Using Deployment echo-no-svc
      Intercept name    : echo-no-svc
      State             : ACTIVE
      Workload kind     : Deployment
      Destination       : 127.0.0.1:8080
      Volume Mount Point: /tmp/telfs-3306285526
      Intercepting      : all TCP connections
      Address           : 10.244.0.13:8080
   ```

Note that the response contains an "Address" that you can curl to reach the intercepted pod. You will not be able to
curl the name "echo-no-svc". Since there's no service by that name, there's no DNS entry for it either.

6. Curl the intercepted workload:

   ```console
   $ curl 10.244.0.13:8080
   < output from your local service>
   ```

> [!IMPORTANT]
> A service-less intercept utilizes an `initContainer` that requires `NET_ADMIN` capabilities.
> If your cluster administrator has disabled them, you will only be able to intercept services using symbolic target ports.

## Specifying the intercept traffic target

By default, it's assumed that your local app is reachable on `127.0.0.1`, and intercepted traffic will be sent to that IP
at the port given by `--port`. If you wish to change this behavior and send traffic to a different IP address, you can use the `--address` parameter
to `telepresence intercept`. Say your machine is configured to respond to HTTP requests for an intercept on `172.16.0.19:8080`. You would run this as:

```console
$ telepresence intercept my-service --address 172.16.0.19 --port 8080
Using Deployment echo-easy
   Intercept name         : echo-easy
   State                  : ACTIVE
   Workload kind          : Deployment
   Destination            : 172.16.0.19:8080
   Service Port Identifier: proxied
   Volume Mount Point     : /var/folders/j8/kzkn41mx2wsd_ny9hrgd66fc0000gp/T/telfs-517018422
   Intercepting           : all TCP connections
```

## Replacing a running workload

By default, your application container continues to run while Telepresence intercepts its traffic. This can cause issues
for applications with ongoing background activities, such as consuming from a message queue.

To address this, the `telepresence intercept` command provides the `--replace` flag. When used, the Traffic Agent
replaces the application container within the pod. This ensures that the application itself is not running and avoids
unintended side effects. The original application container is automatically restored once the intercept session ends.

```console
$ telepresence intercept my-service --port 8080 --replace
   Intercept name         : my-service
   State                  : ACTIVE
   Workload kind          : Deployment
   Destination            : 127.0.0.1:8080
   Service Port Identifier: proxied
   Volume Mount Point     : /var/folders/j8/kzkn41mx2wsd_ny9hrgd66fc0000gp/T/telfs-517018422
   Intercepting           : all TCP connections
```

> [!NOTE]
> Sidecars will not be stopped. Only the targeted container will be removed from the pod.
