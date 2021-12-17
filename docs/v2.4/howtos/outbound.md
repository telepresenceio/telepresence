---
description: "Telepresence can connect to your Kubernetes cluster, letting you access cluster services as if your laptop was another pod in the cluster."
---

import Alert from '@material-ui/lab/Alert';

# Proxy outbound traffic to my cluster

While preview URLs are a powerful feature, Telepresence offers other options for proxying traffic between your laptop and the cluster. This section discribes how to proxy outbound traffic and control outbound connectivity to your cluster.

<Alert severity="info"> This guide assumes that you have the <a href="../../quick-start/demo-node/">quick start</a> sample web app running in your cluster to test accessing the <code>web-app</code> service. You can substitute this service for any other service you are running.</Alert>

## Proxying outbound traffic

Connecting to the cluster instead of running an intercept allows you to access cluster workloads as if your laptop was another pod in the cluster. This enables you to access other Kubernetes services using `<service name>.<namespace>`. A service running on your laptop can interact with other services on the cluster by name.

When you connect to your cluster, the background daemon on your machine runs and installs the [Traffic Manager deployment](../../reference/architecture/) into the cluster of your current `kubectl` context.  The Traffic Manager handles the service proxying.

1. Run `telepresence connect` and enter your password to run the daemon.

  ```
  $ telepresence connect
  Launching Telepresence Daemon v2.3.7 (api v3)
  Need root privileges to run "/usr/local/bin/telepresence daemon-foreground /home/<user>/.cache/telepresence/logs '' ''"
  [sudo] password:
  Connecting to traffic manager...
  Connected to context default (https://<cluster public IP>)
  ```

2. Run `telepresence status` to confirm connection to your cluster and that it is proxying traffic.

  ```
  $ telepresence status
  Root Daemon: Running
    Version     : v2.3.7 (api 3)
    Primary DNS : ""
    Fallback DNS: ""
  User Daemon: Running
    Version           : v2.3.7 (api 3)
    Ambassador Cloud  : Logged out
    Status            : Connected
    Kubernetes server : https://<cluster public IP>
    Kubernetes context: default
    Telepresence proxy: ON (networking to the cluster is enabled)
    Intercepts        : 0 total
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
    Telepresence Daemon quitting...done
  ```  

<Alert severity="info">When using Telepresence in this way, you need to access services with the namespace qualified DNS name (<code>&lt;service name&gt;.&lt;namespace&gt;</code>) before you start an intercept. After you start an intercept, only  <code>&lt;service name&gt;</code> is required. Read more about these differences in the  <a href="../../quick-start/demo-node/">DNS resolution reference guide</a>.</Alert>

## Controlling outbound connectivity

By default, Telepresence provides access to all Services found in all namespaces in the connected cluster. This can lead to problems if the user does not have RBAC access permissions to all namespaces. You can use the `--mapped-namespaces <comma separated list of namespaces>` flag to control which namespaces are accessible.

When you use the `--mapped-namespaces` flag, you need to include all namespaces containing services you want to access, as well as all namespaces that contain services related to the intercept.

### Using local-only intercepts

When you develop on isolated apps or on a virtualized container, you don't need an outbound connection. However, when developing services that aren't deployed to the cluster, it can be necessary to provide outbound connectivity to the namespace where the service will be deployed. This is because services that aren't exposed through ingress controllers require connectivity to those services. When you provide outbound connectivity, the service can access other services in that namespace without using qualified names. A local-only intercept does not cause outbound connections to originate from the intercepted namespace. The reason for this is to establish correct origin; the connection must be routed to a `traffic-agent`of an intercepted pod. For local-only intercepts, the outbound connections originates from the `traffic-manager`.

To control outbound connectivity to specific namespaces, add the `--local-only` flag:

  ```
    $ telepresence intercept <deployment name> --namespace <namespace> --local-only
  ```
The resources in the given namespace can now be accessed using unqualified names as long as the intercept is active. 
You can deactivate the intercept with `telepresence leave <deployment name>`. This removes unqualified name access.

### Proxy outcound connectivity for laptops

To specify additional hosts or subnets that should be resolved inside of the cluster, see [AlsoProxy](../../reference/config/#alsoproxy) for more details.