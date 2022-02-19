import Alert from '@material-ui/lab/Alert';

# Intercepts

When intercepting a service, Telepresence installs a *traffic-agent*
sidecar in to the workload.  That traffic-agent supports one or more
intercept *mechanisms* that it uses to decide which traffic to
intercept.  Telepresence has a simple default traffic-agent, however
you can configure a different traffic-agent with more sophisticated
mechanisms either by setting the [`images.agentImage` field in
`config.yml`](../config/#images) or by writing an
[`extensions/${extension}.yml`][extensions] file that tells
Telepresence about a traffic-agent that it can use, what mechanisms
that traffic-agent supports, and command-line flags to expose to the
user to configure that mechanism.  You may tell Telepresence which
known mechanism to use with the `--mechanism=${mechanism}` flag or by
setting one of the `--${mechansim}-XXX` flags, which implicitly set
the mechanism; for example, setting `--http-match=auto` implicitly
sets `--mechanism=http`.

The default open-source traffic-agent only supports the `tcp`
mechanism, which treats the raw layer 4 TCP streams as opaque and
sends all of that traffic down to the developer's workstation.  This
means that it is a "global" intercept, affecting all users of the
cluster.

In addition to the default open-source traffic-agent, Telepresence
already knows about the Ambassador Cloud
[traffic-agent][ambassador-agent], which supports the `http`
mechanism.  The `http` mechanism operates at higher layer, working
with layer 7 HTTP, and may intercept specific HTTP requests, allowing
other HTTP requests through to the regular service.  This allows for
"personal" intercepts which only intercept traffic tagged as belonging
to a given developer.

[extensions]: https://pkg.go.dev/github.com/telepresenceio/telepresence/v2@v$version$/pkg/client/cli/extensions
[ambassador-agent]: https://github.com/telepresenceio/telepresence/blob/release/v2/pkg/client/cli/extensions/builtin.go#L30-L50

## Intercept behavior when logged in to Ambassador Cloud

Logging in to Ambassador Cloud (with [`telepresence
login`](../client/login/)) changes the Telepresence defaults in two
ways.

First, being logged in to Ambassador Cloud causes Telepresence to
default to `--mechanism=http --http-match=auto --http-path-prefix=/` (
`--mechanism=http` is redundant. It is implied by other `--http-xxx` flags).
If you hadn't been logged in it would have defaulted to
`--mechanism=tcp`.  This tells Telepresence to use the Ambassador
Cloud traffic-agent to do smart "personal" intercepts and only
intercept a subset of HTTP requests, rather than just intercepting the
entirety of all TCP connections.  This is important for working in a
shared cluster with teammates, and is important for the preview URL
functionality below.  See `telepresence intercept --help` for
information on using the `--http-match` and `--http-path-xxx` flags to
customize which requests that are intercepted.

Secondly, being logged in causes Telepresence to default to
`--preview-url=true`.  If you hadn't been logged in it would have
defaulted to `--preview-url=false`.  This tells Telepresence to take
advantage of Ambassador Cloud to create a preview URL for this
intercept, creating a shareable URL that automatically sets the
appropriate headers to have requests coming from the preview URL be
intercepted.  In order to create the preview URL, it will prompt you
for four settings about how your cluster's ingress is configured.  For
each, Telepresence tries to intelligently detect the correct value for
your cluster; if it detects it correctly, may simply press "enter" and
accept the default, otherwise you must tell Telepresence the correct
value.

When creating an intercept with the `http` mechanism, the
traffic-agent sends a `GET /telepresence-http2-check` request to your
service and to the process running on your local machine at the port
specified in your intercept, in order to determine if they support
HTTP/2.  This is required for the intercepts to behave correctly.  If
you do not have a service running locally when the intercept is
created, the traffic-agent will use the result it got from checking
the in-cluster service.

## Supported workloads

Kubernetes has various
[workloads](https://kubernetes.io/docs/concepts/workloads/).
Currently, Telepresence supports intercepting (installing a
traffic-agent on) `Deployments`, `ReplicaSets`, and `StatefulSets`.

<Alert severity="info">

While many of our examples use Deployments, they would also work on
ReplicaSets and StatefulSets

</Alert>

## Specifying a namespace for an intercept

The namespace of the intercepted workload is specified using the
`--namespace` option.  When this option is used, and `--workload` is
not used, then the given name is interpreted as the name of the
workload and the name of the intercept will be constructed from that
name and the namespace.

```shell
telepresence intercept hello --namespace myns --port 9000
```

This will intercept a workload named `hello` and name the intercept
`hello-myns`.  In order to remove the intercept, you will need to run
`telepresence leave hello-mydns` instead of just `telepresence leave
hello`.

The name of the intercept will be left unchanged if the workload is specified.

```shell
telepresence intercept myhello --namespace myns --workload hello --port 9000
```

This will intercept a workload named `hello` and name the intercept `myhello`.

## Importing environment variables

Telepresence can import the environment variables from the pod that is
being intercepted, see [this doc](../environment/) for more details.

## Creating an intercept without a preview URL

If you *are not* logged in to Ambassador Cloud, the following command
will intercept all traffic bound to the service and proxy it to your
laptop. This includes traffic coming through your ingress controller,
so use this option carefully as to not disrupt production
environments.

```shell
telepresence intercept <deployment name> --port=<TCP port>
```

If you *are* logged in to Ambassador Cloud, setting the
`--preview-url` flag to `false` is necessary.

```shell
telepresence intercept <deployment name> --port=<TCP port> --preview-url=false
```

This will output an HTTP header that you can set on your request for
that traffic to be intercepted:

```console
$ telepresence intercept <deployment name> --port=<TCP port> --preview-url=false
Using Deployment <deployment name>
intercepted
    Intercept name: <full name of intercept>
    State         : ACTIVE
    Workload kind : Deployment
    Destination   : 127.0.0.1:<local TCP port>
    Intercepting  : HTTP requests that match all of:
      header("x-telepresence-intercept-id") ~= regexp("<uuid unique to you>:<full name of intercept>")
```

Run `telepresence status` to see the list of active intercepts.

```console
$ telepresence status
Root Daemon: Running
  Version     : v2.1.4 (api 3)
  Primary DNS : ""
  Fallback DNS: ""
User Daemon: Running
  Version           : v2.1.4 (api 3)
  Ambassador Cloud  : Logged out
  Status            : Connected
  Kubernetes server : https://<cluster public IP>
  Kubernetes context: default
  Telepresence proxy: ON (networking to the cluster is enabled)
  Intercepts        : 1 total
    dataprocessingnodeservice: <laptop username>@<laptop name>
```

Finally, run `telepresence leave <name of intercept>` to stop the intercept.

## Skipping the ingress dialogue

You can skip the ingress dialogue by setting the relevant parameters using flags. If any of the following flags are set, the dialogue will be skipped and the flag values will be used instead. If any of the required flags are missing, an error will be thrown.

| Flag             | Description                                                      | Required   |
|------------------|------------------------------------------------------------------|------------|
| `--ingress-host` | The ip address for the ingress                                   | yes        |
| `--ingress-port` | The port for the ingress                                         | yes        |
| `--ingress-tls`  | Whether tls should be used                                       | no         |
| `--ingress-l5`   | Whether a different ip address should be used in request headers | no         |

## Creating an intercept when a service has multiple ports

If you are trying to intercept a service that has multiple ports, you
need to tell Telepresence which service port you are trying to
intercept.  To specify, you can either use the name of the service
port or the port number itself.  To see which options might be
available to you and your service, use kubectl to describe your
service or look in the object's YAML. For more information on multiple
ports, see the [Kubernetes documentation][kube-multi-port-services].

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
