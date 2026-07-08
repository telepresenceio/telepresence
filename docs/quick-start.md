---
title: Quick start
description: "Connect to a cluster, deploy a sample service, and replace it with a copy running on your workstation - in about ten minutes."
hide_table_of_contents: true
---

# Telepresence Quick Start

In this guide you will connect your workstation to a Kubernetes cluster, deploy a
small sample service, and then replace that service with a copy running on your
workstation — while the rest of the cluster keeps talking to it as if nothing had
changed. That round trip is the core of what Telepresence does: it lets you run a
service locally, with your own tools, IDE, and debugger, as a full member of the
remote cluster.

Expect the whole guide to take about ten minutes.

## Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/) (or `oc` for
  OpenShift) with access to a cluster. Any cluster works: Docker Desktop,
  minikube, kind, or a remote one. You need permission to install the
  traffic-manager (a one-time, cluster-admin operation).
- The [Telepresence client](install/client.md) installed on your workstation.
- Docker, used here to run the sample service locally. When you work with your
  own services, anything that listens on a local port works the same way.

## 1. Install the traffic manager

The traffic-manager is Telepresence's cluster-side component. Install it once per
cluster:

```console
$ telepresence helm install
Traffic Manager installed successfully
```

See [Install Traffic Manager](install/manager.md) for custom namespaces, Helm
values, and installation as part of your own charts.

## 2. Deploy the sample service

Deploy an echo server. It responds to every HTTP request with a line that names
the host that served it — which is how you will see, later on, exactly where your
requests end up:

```console
$ kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello
  labels:
    app: hello
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hello
  template:
    metadata:
      labels:
        app: hello
    spec:
      containers:
        - name: echo-server
          image: ghcr.io/telepresenceio/echo-server:latest
          ports:
            - name: http
              containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: hello
spec:
  selector:
    app: hello
  ports:
    - port: 80
      targetPort: http
EOF
deployment.apps/hello created
service/hello created

$ kubectl rollout status deployment hello
deployment "hello" successfully rolled out
```

## 3. Connect to the cluster

```console
$ telepresence connect --namespace default
Launching Telepresence User Daemon
Launching Telepresence Root Daemon
Connected to context default, namespace default (https://<cluster-api-url>)
```

> [!NOTE]
> If Telepresence was installed as a standalone binary rather than with a
> [package installer](install/client.md), this step asks for your password:
> starting the root daemon requires elevated privileges.

Telepresence has now set up a virtual network interface and a DNS resolver that
make the cluster's services reachable from your workstation, for every local
tool — `curl`, your browser, your IDE.

## 4. Reach the service like a pod would

Use the service's cluster DNS name, just as another pod in the namespace would:

```console
$ curl http://hello
Request served by hello-69fbdc98cf-4bnkl
...
```

The response names the pod that served the request. So far, everything runs in
the cluster.

## 5. Run the service locally

Start the same echo server on your workstation. The `--hostname` flag makes its
responses recognizable:

```console
$ docker run --rm --detach --name hello-local --hostname my-workstation -p 8080:8080 ghcr.io/telepresenceio/echo-server:latest
$ curl http://localhost:8080
Request served by my-workstation
...
```

Two copies of the service are now running: one in the cluster, one locally. In
real development, this local copy is your work-in-progress code, started from
your IDE or a debugger.

## 6. Replace the cluster container with your local one

```console
$ telepresence replace hello
Using Deployment hello
   Container name : echo-server
   State          : ACTIVE
   Workload kind  : Deployment
   Port forwards  : 10.1.4.106 -> 127.0.0.1
       8080 -> 8080 TCP
```

The container in the cluster has been replaced by a Telepresence traffic-agent
that forwards everything to your workstation. Ask the cluster service again:

```console
$ curl http://hello
Request served by my-workstation
...
```

The request went to the cluster service, but your local process answered it.
Anything that talks to `hello` inside the cluster — other services, ingress
traffic, batch jobs — now reaches your workstation, and your local process can in
turn reach everything the replaced container could. Telepresence can also hand
you the container's environment variables and volumes; see
[Code and debug an application locally](howtos/attach.md).

Replace is one of four ways to [attach to a workload](concepts/attachments.md).
The others are *intercept*
(reroute traffic to a specific service port while the remote container keeps
running), *wiretap* (receive a copy of the traffic without disturbing the
workload), and *ingest* (get the container's environment and volumes, no traffic
involved).

## 7. Detach

```console
$ telepresence detach hello
```

The original container is restored, and the pod answers again:

```console
$ curl http://hello
Request served by hello-69fbdc98cf-4bnkl
...
```

## 8. Clean up

```console
$ telepresence quit
$ docker stop hello-local
$ kubectl delete service,deployment hello
```

The traffic-manager can stay installed for next time; remove it with
`telepresence helm uninstall` if you prefer.

## What's next?

- [Code and debug an application locally](howtos/attach.md) — the four attachment
  modes in depth, with environment variables and volume mounts.
- [Use Telepresence with Docker](howtos/docker.md) — run the Telepresence daemon
  itself in a container, no root access needed.
- [Architecture](concepts/architecture.md) — how the pieces fit together.
