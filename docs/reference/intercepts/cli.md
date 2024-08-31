import Alert from '@material-ui/lab/Alert';

# Configuring intercept using CLI

## Specifying a namespace for an intercept

The namespace of the intercepted workload is specified during connect using the `--namespace` option.

```shell
telepresence connect --namespace myns
telepresence intercept hello --port 9000
```

## Importing environment variables

Telepresence can import the environment variables from the pod that is
being intercepted, see [this doc](../environment/) for more details.

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
a new intercept the same way you did above and it will change which
service port is being intercepted.

## Creating an intercept When multiple services match your workload

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
by creating more than one intercept that identify the same workload using the `--workload` flag.

Let's assume that we have a service `multi-echo` with the two ports `http` and `grpc`. They are both
targeting the same `multi-echo` deployment.

```console
$ telepresence intercept multi-echo-http --workload multi-echo --port 8080:http
Using Deployment multi-echo
intercepted
    Intercept name         : multi-echo-http
    State                  : ACTIVE
    Workload kind          : Deployment
    Destination            : 127.0.0.1:8080
    Service Port Identifier: http
    Volume Mount Point     : /tmp/telfs-893700837
    Intercepting           : all TCP requests
$ telepresence intercept multi-echo-grpc --workload multi-echo --port 8443:grpc --mechanism tcp
Using Deployment multi-echo
intercepted
    Intercept name         : multi-echo-grpc
    State                  : ACTIVE
    Workload kind          : Deployment
    Destination            : 127.0.0.1:8443
    Service Port Identifier: extra
    Volume Mount Point     : /tmp/telfs-1277723591
    Intercepting           : all TCP requests
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

<Alert severity="info">
This utilizes an <code>initContainer</code> that requires `NET_ADMIN` capabilities.
If your cluster administrator has disabled them, you will be unable to use numeric ports with the agent injector.
</Alert>

<Alert severity="info">
This requires the Traffic Agent to run as GID <code>7777</code>. By default, this is disabled on openshift clusters.
To enable running as GID <code>7777</code> on a specific openshift namespace, run:
<code>oc adm policy add-scc-to-group anyuid system:serviceaccounts:$NAMESPACE</code>
</Alert>

<Alert severity="info">
Intercepting headless services without a selector is not supported.
</Alert>

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

By default, your application keeps running as Telepresence intercepts it, even if it doesn't receive
any traffic (or receives only a subset, as with personal intercepts). This can pose a problem for applications that are active
even when they're not receiving requests. For instance, if your application consumes from a message queue as soon as it
starts up, intercepting it won't stop the pod from consuming from the queue.

To work around this issue, `telepresence intercept` allows you to pass in a `--replace` flag that will stop every
application container from running on your pod. When you pass in `--replace`, Telepresence will restart your application
with a dummy application container that sleeps infinitely, and instead just place a traffic agent to redirect traffic to
your local machine. The application container will be restored as soon as you leave the intercept. 

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

<Alert severity="info">
Using the --replace flag implies a global intercept. This is to prevent situations
where multiple personal intercepts are consuming from the same message queue, which
would be the same as allowing the application to do so while an intercept is running.
</Alert>

<Alert severity="info">
Sidecars will not be stopped. Only the container serving the intrcepted port will be removed from the pod.
</Alert>
