---
description: "Telepresence can connect to your Kubernetes cluster, letting you access cluster services as if your laptop was another pod in the cluster."
---

import Alert from '@material-ui/lab/Alert';

# Proxy outbound traffic to my cluster

While preview URLs are a powerful feature, there are other options to use Telepresence for proxying traffic between your laptop and the cluster.

<Alert severity="info"> We'll assume below that you have the <a href="../../quick-start/qs-node/">quick start</a> sample web app running in your cluster so that we can test accessing the <code>verylargejavaservice</code> service. That service can be substituted however for any service you are running.</Alert>

## Proxying outbound traffic

Connecting to the cluster instead of running an intercept will allow you to access cluster workloads as if your laptop was another pod in the cluster. You will be able to access other Kubernetes services using `<service name>.<namespace>`, for example by curling a service from your terminal. A service running on your laptop will also be able to interact with other services on the cluster by name.

Connecting to the cluster starts the background daemon on your machine and installs the [Traffic Manager pod](../../reference/architecture/) into the cluster of your current `kubectl` context.  The Traffic Manager handles the service proxying.

1. Run `telepresence connect`, you will be prompted for your password to run the daemon.

  ```
  $ telepresence connect
  Launching Telepresence Daemon v2.1.4 (api v3)
  Need root privileges to run "/usr/local/bin/telepresence daemon-foreground /home/<user>/.cache/telepresence/logs '' ''"
  [sudo] password:
  Connecting to traffic manager...
  Connected to context default (https://<cluster public IP>)
  ```

1. Run `telepresence status` to confirm that you are connected to your cluster and are proxying traffic to it.

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
    Intercepts        : 0 total
  ```

1. Now try to access your service by name with `curl verylargejavaservice.default:8080`. Telepresence will route the request to the cluster, as if your laptop is actually running in the cluster.

  ```
  $ curl verylargejavaservice.default:8080
  <!DOCTYPE HTML>
  <html>
  <head>
      <title>Welcome to the EdgyCorp WebApp</title>
  ...
  ```

3. Terminate the client with `telepresence quit` and try to access the service again, it will fail because traffic is no longer being proxied from your laptop.

  ```
  $ telepresence quit
  Telepresence Daemon quitting...done
  ```

<Alert severity="info">When using Telepresence in this way, services must be accessed with the namespace qualified DNS name (<code>&lt;service name&gt;.&lt;namespace&gt;</code>) before starting an intercept.  After starting an intercept, only <code>&lt;service name&gt;</code> is required. Read more about these differences in DNS resolution <a href="../../reference/dns/">here</a>.</Alert>

## Controlling outbound connectivity

By default, Telepresence will provide access to all Services found in all namespaces in the connected cluster. This might lead to problems if the user does not have access permissions to all namespaces via RBAC. The `--mapped-namespaces <comma separated list of namespaces>` flag was added to give the user control over exactly which namespaces will be accessible.

When using this option, it is important to include all namespaces containing services to be accessed and also all namespaces that contain services that those intercepted services might use.

### Using local-only intercepts

An intercept with the flag`--local-only` can be used to control outbound connectivity to specific namespaces.

When developing services that have not yet been deployed to the cluster, it can be necessary to provide outbound connectivity to the namespace where the service is intended to be deployed so that it can access other services in that namespace without using qualified names. 

  ```
  $ telepresence intercept <deployment name> --namespace <namespace> --local-only
  ```
The resources in the given namespace can now be accessed using unqualified names as long as the intercept is active. The intercept is deactivated just like any other intercept.

  ```
  $ telepresence leave <deployment name>
  ```
The unqualified name access is now removed provided that no other intercept is active and using the same namespace.

### External dependencies (formerly `--also-proxy`)

If you have a resource outside of the cluster that you need access to, you can leverage Headless Services to provide access. This will give you a kubernetes service formatted like all other services (`my-service.prod.svc.cluster.local`), that resolves to your resource.

If the outside service has a DNS name, you can use the [ExternalName](https://kubernetes.io/docs/concepts/services-networking/service/#externalname) service type, which will create a service that can be used from within your cluster and from your local machine when connected with telepresence.

If the outside service is an ip, create a [service without selectors](https://kubernetes.io/docs/concepts/services-networking/service/#services-without-selectors) and then create an endpoint of the same name.

In both scenarios, Kubernetes will create a service that can be used from within your cluster and from your local machine when connected with telepresence.
