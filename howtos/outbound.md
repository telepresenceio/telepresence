---
description: "Telepresence can connect to your Kubernetes cluster, letting you access cluster services as if your laptop was another pod in the cluster."
---

# Outbound Sessions

While preview URLs are a powerful feature, there are other options to use Telepresence for proxying traffic between your laptop and the cluster.

## Prerequistes

It is assumed that you have the demo web app from the [tutorial](../../tutorial/) running in your cluster, but deployment names used below can be substituted for any other running deployment.

## Proxying Outbound Traffic

Connecting to the cluster instead of running an intercept will allow you to access cluster deployments as if your laptop was another pod in the cluster. You will be able to access other Kubernetes services by their [short DNS name](https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/), for example by curling a service from your terminal. A service running on your laptop will also be able to interact with other services on the cluster by name.

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

1. Now try to access your service by name with `curl verylargejavaservice`. Telepresence will route the request to the cluster, as if your laptop is actually running in the cluster.

  ```
  $ curl verylargejavaservice:8080
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
