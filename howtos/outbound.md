---
description: "Telepresence can connect to your Kubernetes cluster, letting you access cluster services as if your laptop was another pod in the cluster."
---

# Outbound Sessions

While preview URLs are a powerful feature, there are other options to use Telepresence for proxying traffic between your laptop and the cluster.

## Prerequistes

It is assumed that you have the demo web app from the [tutorial](../../tutorial/) running in your cluster, but deployment names used below can be substituted for any other running deployment.

## Proxying Outbound Traffic

Connecting to the cluster instead of running an intercept will allow you to access cluster deployments as if your laptop was another pod in the cluster. You will be able to access other Kubernetes services using `<servicename>.<namespace>`, for example by curling a service from your terminal. A service running on your laptop will also be able to interact with other services on the cluster by name.

Connecting to the cluster starts the background daemon on your machine and installs the [Traffic Manager pod](../../reference/) into the cluster of your current `kubectl` context.  The Traffic Manager handles the service proxying.

1. Run `telepresence connect`, you will be prompted for your password to run the daemon.

  ```
  $ telepresence connect
  Launching Telepresence Daemon v2.0.0 (api v3)
  Need root privileges to run "/usr/local/bin/telepresence daemon-foreground"
  Password:
  Connecting to traffic manager...
  Connected to context default (https://<cluster-public-IP>)
  ```

1. Run `telepresence status` to confirm that you are connected to your cluster and are proxying traffic to it.

  ```
  $ telepresence status
  Connected
    Context:       default (https://<cluster-public-IP>)
    Proxy:         ON (networking to the cluster is enabled)
    Intercepts:    0 total
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

## Controlling Outbound Connectivity

By default, Telepresence will provide access to all Services found in all namespaces in the connected cluster. This might lead to problems if the user does not have access permissions to all namespaces via RBAC. The `--mapped-namespaces <comma separated list of namespaces>` flag was added to give the user control over exactly which namespaces will be accessible.

When using this option, it is important to include all namespaces containing services to be accessed and also all namespaces that contain services that those intercepted services might use.

### Using local-only intercepts

An intercept with the flag`--local-only` can be used to control outbound connectivity to specific namespaces.

When developing services that have not yet been deployed to the cluster, it can be necessary to provide outbound connectivity to the namespace where the service is intended to be deployed so that it can access other services in that namespace without using qualified names. 

  ```
  $ telepresence intercept [name of intercept] --namespace [name of namespace] --local-only
  ```
The resources in the given namespace can now be accessed using unqualified names as long as the intercept is active. The intercept is deactivated just like any other intercept.

  ```
  $ telepresence leave [name of intercept]
  ```
The unqualified name access is now removed provided that no other intercept is active and using the same namespace.

### External dependencies (formerly --also-proxy)
If you have a resource outside of the cluster that you need access to, you can leverage Headless Services to provide access. This will give you a kubernetes service formatted like all other services (`my-service.prod.svc.cluster.local`), that resolves to your resource.

If the outside service has a DNS name, you can use the [ExternalName](https://kubernetes.io/docs/concepts/services-networking/service/#externalname) service type, which will create a service that can be used from within your cluster and from your local machine when connected with telepresence.

If the outside service is an ip, create a [service without selectors](https://kubernetes.io/docs/concepts/services-networking/service/#services-without-selectors) and then create an endpoint of the same name.

In both scenarios, kubernetes will create a service that can be used from within your cluster and from your local machine when connected with telepresence.
