# Intercepts 

## Intercept Behavior When Logged into Ambassador Cloud

After logging into Ambassador Cloud (with `telepresence login`), Telepresence will default to `--preview-url=true`, which will use Ambassador Cloud to create a sharable preview URL for this intercept. (Creating an intercept without logging in will default to `--preview-url=false`).

In order to do this, it will prompt you for four options.  For the first, `Ingress`, Telepresence tries to intelligently determine the ingress controller deployment and namespace for you.  If they are correct, you can hit `enter` to accept the defaults.  Set the next two options, `TLS` and `Port`, appropriately based on your ingress service. The fourth is a hostname for the service, if required by your ingress.

Also because you're logged in, Telepresence will default to `--mechanism=http --http-match=auto` (or just `--http-match=auto`; `--http-match` implies `--mechanism=http`). If you hadn't been logged in it would have defaulted to `--mechanism=tcp`.  This tells it to do smart intercepts and only intercept a subset of HTTP requests, rather than just intercepting the entirety of all TCP connections.  This is important for working in a shared cluster with teammates, and is important for the preview URL functionality.  See `telepresence intercept --help` for information on using `--http-match` to customize which requests it intercepts.

## Supported Workloads
Kubernetes has various [workloads](https://kubernetes.io/docs/concepts/workloads/). Currently, telepresence supports intercepting Deployments, ReplicaSets, and StatefulSets.
<Alert severity="info"> While many of our examples may use Deployments, they would also work on ReplicaSets and StatefulSets </Alert>

## Specifying a namespace for an intercept

The namespace of the intercepted workload is specified using the `--namespace` option. When this option is used, and `--workload` is not used, then the given name is interpreted as the name of the workload and the name of the intercept will be constructed from that name and the namespace.

```
telepresence intercept hello --namespace myns --port 9000
```

This will intercept a workload named "hello" and name the intercept
"hello-myns".  In order to remove the intercept, you will need to run
`telepresence leave hello-mydns` instead of just `telepresence leave
hello`.

The name of the intercept will be left unchanged if the workload is specified.

```
telepresence intercept myhello --namespace myns --workload hello --port 9000
```

This will intercept a workload named "hello" and name the intercept "myhello".

## Importing Environment Variables

Telepresence can import the environment variables from the pod that is being intercepted, see [this doc](../environment/) for more details.

## Creating an Intercept Without a Preview URL

If you *are not* logged into Ambassador Cloud, the following command will intercept all traffic bound to the service and proxy it to your laptop. This includes traffic coming through your ingress controller, so use this option carefully as to not disrupt production environments.

```
telepresence intercept <deployment name> --port=<TCP port>
```

If you *are* logged into Ambassador Cloud, setting the `preview-url` flag to `false` is necessary.

```
telepresence intercept <deployment name>  --port=<TCP port> --preview-url=false
```

This will output a header that you can set on your request for that traffic to be intercepted:

```
$ telepresence intercept <deployment name>  --port=<TCP port> --preview-url=false
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

```
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

## Creating an Intercept When a Service has Multiple Ports

If you are trying to intercept a service that has multiple ports, you need to tell telepresence which service port you are trying to intercept. To specifiy, you can either use the name of the service port or the port number itself. To see which options might be available to you and your service, use kubectl to describe your service or look in the objects yaml. For more information on multiple ports, see the [Kubernetes documentation](https://kubernetes.io/docs/concepts/services-networking/service/#multi-port-services).

```
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

When intercepting a service that has multiple ports, the name of the service port that has been intercepted is also listed.

If you want to change which port has been intercepted, you can create a new intercept the same way you did above and it will change which service port is being intercepted.

## Creating an Intercept When Multiple Services Match your Workload

Oftentimes, there's a 1-to-1 relationship between a service and a workload, so telepresence is able to auto-detect which service it should intercept based on the workload you are trying to intercept.  But if you use something like [Argo](../../../../argo/latest/), it uses two services (that use the same labels) to manage traffic between a canary and a stable service.

Fortunately, if you know which service you want to use when intercepting a workload, you can use the --service flag.  So in the aforementioned demo, if you wanted to use the `echo-stable` service when intercepting your workload, your command would look like this:
```
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
