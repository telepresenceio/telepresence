---
title: Code and debug an application locally
description: Start using Telepresence in your own environment. Follow these steps to work locally with cluster applications.
hide_table_of_contents: true
---

# Code and debug an application locally

## Four ways to attach

Telepresence offers four ways to attach to a workload and run your service
locally against the cluster: **replace**, **intercept**, **wiretap**, and
**ingest**. They differ in how traffic is routed, whether the remote container
keeps running, and how volumes are shared; see
[Attachments](../concepts/attachments.md) for a comparison and guidance on
choosing. This guide walks through using each of them.

## Prerequisites

Before you begin, you need to have [Telepresence installed](../install/client.md). This document uses the Kubernetes command-line tool, [`kubectl`](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
in several examples. OpenShift users can substitute oc [commands instead](https://docs.openshift.com/container-platform/4.1/cli_reference/developer-cli-commands.html).

This guide assumes you have an application represented by a Kubernetes deployment and service accessible publicly by an ingress controller,
and that you can run a copy of that application on your laptop.

## Replace Your Container

This approach offers the benefit of direct cluster connectivity from your workstation, simplifying debugging and
modification of your application within its familiar environment. Note that if Telepresence was installed using a
standalone binary rather than a [package installer](../install/client.md), it will require root access to configure the
network interface. Remote mounts must be made relative to a specific mount point, which can add complexity.

1. Connect to your cluster with `telepresence connect` and try to curl to the Kubernetes API server. A 401 or 403 response code is expected and indicates that the service could be reached:

   ```console
   $ curl -ik https://kubernetes.default
   HTTP/1.1 401 Unauthorized
   Cache-Control: no-cache, private
   Content-Type: application/json
   ...
   ```

   You now have access to your remote Kubernetes API server as if you were on the same network. You can now use any local tools to connect to any service in the cluster.

2. Enter `telepresence list` and make sure the workload (deployment in this case) you want to intercept is listed. For example:

   ```console
   $ telepresence list
   ...
   deployment example-app: ready to attach (traffic-agent not yet installed)
   ...
   ```

3. Get the name of the container you want to replace (output truncated for brevity)
    ```console
    $ kubectl describe deploy example-app
    Name:                   example-app
    Namespace:              default
    CreationTimestamp:      Tue, 14 Jan 2025 03:49:29 +0100
    Labels:                 app=example-app
    Annotations:            deployment.kubernetes.io/revision: 1
    Selector:               app=example-app
    Replicas:               1 desired | 1 updated | 1 total | 0 available | 1 unavailable
    StrategyType:           RollingUpdate
    MinReadySeconds:        0
    RollingUpdateStrategy:  25% max unavailable, 25% max surge
    Pod Template:
      Labels:  app=example-app
      Containers:
       echo-server:
        Image:      ghcr.io/telepresenceio/echo-server
        Port:       8080/TCP
    ```

4. Replace the container. Please note that the `--container echo-server` flag here is optional. It's only needed when the workload has more than one container:
   ```console
   $ telepresence replace example-app --container echo-server --env-file /tmp/example-app.env --mount /tmp/example-app-mounts
   Using Deployment example-app
   Container name    : echo-server
   State             : ACTIVE
   Workload kind     : Deployment
   Port forwards     : 10.1.4.106 -> 127.0.0.1
       8080 -> 8080 TCP
   Volume Mount Point: /tmp/example-app-mounts
   ```
   Your workstation is now ready. You can run the application using the environment in the `/tmp/example-app.env` file and the
   mounts under `/tmp/example-app-mounts`. The application can listen to `localhost:8080` to receive traffic intended for the
   replaced container. On the cluster side of things, a Traffic Agent container has replaced the `echo-server`.

   Telepresence assumes that you want all declared container ports to be mapped to their corresponding port on `localhost`. You
   can change this with the `--port` flag. For example, `--port 1080:8080` will map the replaced containers port number `8080`
   to `localhost:1080`. The `--port` can also be used when the container is known to listen to ports that are not declared in
   the manifest.

5. Query the cluster in which you replaced your application and verify your local instance being invoked. All the traffic previously routed to your Kubernetes Service is now routed to your local environment

You can now:
- Make changes on the fly and see them reflected when interacting with your Kubernetes environment.
- Query services only exposed in your cluster's network.
- Set breakpoints in your IDE to investigate bugs.

6. You end the replace operation with the command `telepresence detach example-app --container echo-server`

## Ingest Your Container

In some situations, you want to work and debug the code locally, and you want it to be able to access other services in the cluster,
but you don't wish to interfere with the targeted workload. This is where the `telepresence ingest` command comes into play. Just
like `replace` command, it will make the environment and mounted containers of the targeted container available locally, but it will
not replace the container nor will it intercept any of its traffic.

This example assumes that you have the `example-app` deployment.

1. Connect and run and start an ingest from `example-app`:
   ```console
   $ telepresence connect
   Launching Telepresence User Daemon
   Launching Telepresence Root Daemon
   Connected to context xxx, namespace default (https://<some url>)
   $ telepresence ingest example-app --container echo-server --env-file /tmp/example-app.env --mount /tmp/example-app-mounts
   Using Deployment example-app
      Container name    : echo-server
      Workload kind     : Deployment
      Volume Mount Point: /tmp/example-app-mounts
   ```

2. Start your local application using the environment variables retrieved and the volumes that were mounted in the previous step.

You can now:
- Code and debug your local app while it interacts with other services in your cluster.
- Query services only exposed in your cluster's network.
- Set breakpoints in your IDE to investigate bugs.

## Intercept Your Application

The `telepresence intercept` command allows you to redirect traffic for a specific service to your local workstation.
Compared to the replace command, intercept is less invasive because it: a) enables precise filtering of intercepted
traffic using HTTP headers or paths, and b) allows the original service to continue running, handling all other traffic
and tasks not directly related to the intercepted traffic.

1. Connect to your cluster with `telepresence connect`.

2. Intercept all traffic going to the application's http port in your cluster and redirect to port 8080 on your workstation.
    ```console
    $ telepresence intercept example-app --http-header 'x-user=margret' --http-path-prefix '/api' --port 8080:http --env-file ~/example-app-intercept.env --mount /tmp/example-app-mounts
    Using Deployment example-app
    intercepted
      Intercept name: example-app
      State         : ACTIVE
      Workload kind : Deployment
      Destination   : 127.0.0.1:8080
      Intercepting  : HTTP requests with path-prefix /api and header 'X-User: margret'
    ```

   * For `--http-header`: specify the HTTP header you want to filter on. You can specify multiple headers by repeating the flag. Header-based intercepts take priority over path-only intercepts, so that when multiple intercepts are active on the same workload, requests are evaluated against header-based filters first, then path-only filters. This allows different developers to use header-based personal intercepts (e.g., `x-user=alice`) while others use path-based intercepts (e.g., `--http-path-prefix /admin/`) without conflicts.

   * For '--http-path-prefix': specify the path prefix you want to filter on. You can specify multiple path prefixes by repeating the flag. Path-based intercepts have lower priority than header-based intercepts.

   * For `--port`: specify the port the local instance of your application is running on, and optionally the remote port that you want to intercept. Telepresence will select the remote port automatically when there's only one service port available to access the workload. You must specify the port to intercept when the workload exposes multiple ports. You can do this by specifying the port you want to intercept after a colon in the `--port` argument (like in the example), and/or by specifying the service you want to intercept using the `--service` flag.

   * For `--env-file`: specify a file path for Telepresence to write the environment variables that are set for the targeted container.

3. Start your local application using the environment variables retrieved and the volumes that were mounted in the previous step.

You can now:
- Make changes on the fly and see them reflected when interacting with your Kubernetes environment without affecting other users of the same service.
- Query services that are only exposed in your cluster's network.
- Set breakpoints in your IDE to investigate bugs.

## Wiretap Your Application

You can use the `telepresence wiretap` command when you want to wiretap the traffic for a specific service and send a
copy of it to your workstation. The `wiretap` is less intrusive than the `intercept`, because it does not interfere
with the traffic at all.

1. Connect to your cluster with `telepresence connect`.

2. Put a wiretap on all traffic going to the application's http port in your cluster and send it to port 8080 on your workstation.
    ```console
    $ telepresence wiretap example-app --port 8080:http --env-file ~/example-app-intercept.env --mount /tmp/example-app-mounts
    Using Deployment example-app
    wiretapped
      Wiretap name  : example-app
      State         : ACTIVE
      Workload kind : Deployment
      Destination   : 127.0.0.1:8080
      Intercepting  : all TCP connections
    ```

    * For `--port`: specify the port the local instance of your application is running on, and optionally the remote port
      that you want to wiretap. Telepresence will select the remote port automatically when there's only one service
      port available to access the workload. You must specify the port to wiretap when the workload exposes multiple
      ports. You can do this by specifying the port you want to wiretap after a colon in the `--port` argument (like in
      the example), and/or by specifying the service you want to wiretap using the `--service` flag.

    * For `--env-file`: specify a file path for Telepresence to write the environment variables that are set for the targeted
      container.

3. Start your local application using the environment variables retrieved and the volumes that were mounted in the previous step.

You can now:
- Query services only exposed in your cluster's network.
- Set breakpoints in your IDE to investigate bugs.

### Running Everything Using Docker

This approach confines the Telepresence network interface and remote mounts to a container, and like the
[package installer](../install/client.md) approach, eliminates the need for root access.  Additionally, it allows for precise replication of the target container's volume mounts, using identical 
mount points. However, this method will require docker to get cluster connectivity, and the containerized environment can
present challenges in terms of toolchain integration, debugging, and the overall development workflow.

1. Connect to your cluster with `telepresence connect --docker`. This starts the Telepresence daemon in a docker
   container and ensures that this container has access to the cluster network.
2. Use `telepresence curl` to access the Kubernetes API server from a container.
   A 401 or 403 response code is expected and indicates that the service could be reached. The `telepresence curl` command 
   used will execute a standard `curl` command from a container that shares the network created by the `connect` call:

   ```console
   $ telepresence curl -ik https://kubernetes.default
   HTTP/1.1 401 Unauthorized
   Cache-Control: no-cache, private
   Content-Type: application/json
   ...
   ```

   You now have access to your remote Kubernetes API server as if you were on the same network.

3. Enter `telepresence list` and make sure the workload you want to attach to is listed. For example:

   ```console
   $ telepresence list
   ...
   deployment example-app: ready to attach (traffic-agent not yet installed)
   ...
   ```

4. Use `replace`, `inject`, or `intercept` to attach to the container in combination with the `--docker-run` flag.
   Example using `telepresence replace`

    ```console
    $ telepresence replace example-app --container echo-server --docker-run -- <your local container>
    Using Deployment example-app
    intercepted
      Intercept name: example-app
      State         : ACTIVE
      Workload kind : Deployment
      Destination   : 127.0.0.1:8080
      Intercepting  : all TCP connections
    <output from your local container>
    ```

You can now:
- Make changes on the fly and see them reflected when interacting with your Kubernetes environment; although
  depending on how your local container is configured, this might require that it is rebuilt.
- Query services only exposed in your cluster's network using `telepresence curl`.
- Set breakpoints in a _Remote Debug_ configuration in your IDE to investigate bugs.
