---
title: Proxy outbound traffic to my cluster
description: Telepresence can connect to your Kubernetes cluster, letting you access cluster services as if your laptop was another pod in the cluster.
hide_table_of_contents: true
---

# Proxy outbound traffic to my cluster

Telepresence offers other options for proxying traffic between your laptop and the cluster. This section discribes how to proxy outbound traffic and control outbound connectivity to your cluster.

## Proxying outbound traffic

Connecting to the cluster instead of running an intercept allows you to access cluster workloads as if your laptop was another pod in the cluster. This enables you to access other Kubernetes services using `<service name>.<namespace>`. A service running on your laptop can interact with other services on the cluster by name.

When you connect to your cluster, the background daemon on your machine runs and installs the [Traffic Manager deployment](../reference/architecture.md) into the cluster of your current `kubectl` context.  The Traffic Manager handles the service proxying.

1. Run `telepresence connect` and enter your password to run the daemon.

  ```
  $ telepresence connect
  Launching Telepresence User Daemon
  Launching Telepresence Root Daemon
  Connected to context kind-dev, namespace default (https://<cluster public IP>)
  ```

2. Run `telepresence status` to confirm connection to your cluster and that it is proxying traffic.

  ```
  $ telepresence status 
  OSS User Daemon: Running
    Version           : v2.18.0
    Executable        : /usr/local/bin/telepresence
    Install ID        : 4b1655a6-487f-4af3-a6d3-52f1bc1d1112
    Status            : Connected
    Kubernetes server : https://<cluster public IP>
    Kubernetes context: kind-dev
    Namespace         : default
    Manager namespace : ambassador
    Intercepts        : 0 total
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
    Version      : v2.18.0
    Traffic Agent: docker.io/datawire/tel2:2.18.0
  ```

3. Access your service by name with `curl web-app.emojivoto:80`. Telepresence routes the request to the cluster, as if your laptop is actually running in the cluster.

  ```
  $ curl web-app.emojivoto:80
  <!DOCTYPE html>
  <html>
  <head>
     <meta charset="UTF-8">
     <title>Emoji Vote</title>
  ...
  ```

If you terminate the client with `telepresence quit` and try to access the service again, it will fail because traffic is no longer proxied from your laptop.

  ```
    $ telepresence quit
    Disconnected
  ```

> [!NOTE]
> When using Telepresence in this way, you need to access services with the namespace qualified DNS name (<code>&lt;service name&gt;.&lt;namespace&gt;</code>) before you start an intercept. After you start an intercept, only  <code>&lt;service name&gt;</code> is required.

## Controlling outbound connectivity

### Connected Namespace

The `telepresence connect` command will connect to the default namespace, i.e. the namespace that your
current kubernetes context is configured to use, or a namespace named "default". When connected, you can
access all services in this namespace by just using a single label name of the service.

You can specify which namespace to connect to by using a `--namespace <name>` to the connect command.

### Mapped Namespaces
By default, Telepresence provides access to all Services found in all namespaces in the connected cluster. This can lead to problems if the user does not have RBAC access permissions to all namespaces. You can use the `--mapped-namespaces <comma separated list of namespaces>` flag to control which namespaces are accessible.

When you use the `--mapped-namespaces` flag, you need to include all namespaces containing services you want to access, as well as all namespaces that contain services related to the intercept.

The resources in the given namespace can now be accessed using unqualified names as long as the intercept is active.
You can deactivate the intercept with `telepresence leave <deployment name>`. This removes unqualified name access.
