---
description: "How Telepresence works to intercept traffic from your Kubernetes cluster to code running on your laptop."
---

# Telepresence Architecture

## Telepresence CLI

The Telepresence CLI orchestrates the moving parts on the workstation: it starts the Telepresence Daemons and then acts
as a user-friendly interface to the Telepresence User Daemon.

## Telepresence Daemons
Telepresence has Daemons that run on a developer's workstation and act as the main point of communication to the cluster's
network in order to communicate with the cluster and handle intercepted traffic.

### User-Daemon
The User-Daemon coordinates the creation and deletion of intercepts by communicating with the [Traffic Manager](#traffic-manager).
All requests from and to the cluster go through this Daemon.

### Root-Daemon
The Root-Daemon manages the networking necessary to handle traffic between the local workstation and the cluster by setting up a
[Virtual Network Device](tun-device) (VIF).  For a detailed description of how the VIF manages traffic and why it is necessary
please refer to this blog post:
[Implementing Telepresence Networking with a TUN Device](https://blog.getambassador.io/implementing-telepresence-networking-with-a-tun-device-a23a786d51e9).

## Traffic Manager

The Traffic Manager is the central point of communication between Traffic Agents in the cluster and Telepresence Daemons
on developer workstations. It is responsible for injecting the Traffic Agent sidecar into intercepted pods, proxying all
relevant inbound and outbound traffic, and tracking active intercepts.

The Traffic-Manager is installed, either by a cluster administrator using a Helm Chart, or on demand by the Telepresence
User Daemon. When the User Daemon performs its initial connect, it first checks the cluster for the Traffic Manager
deployment, and if missing it will make an attempt to install it using its embedded Helm Chart.

When an intercept gets created with a Preview URL, the Traffic Manager will establish a connection with Ambassador Cloud
so that Preview URL requests can be routed to the cluster. This allows Ambassador Cloud to reach the Traffic Manager
without requiring the Traffic Manager to be publicly exposed. Once the Traffic Manager receives a request from a Preview
URL, it forwards the request to the ingress service specified at the Preview URL creation.

## Traffic Agent

The Traffic Agent is a sidecar container that facilitates intercepts. When an intercept is first started, the Traffic Agent
container is injected into the workload's pod(s). You can see the Traffic Agent's status by running `telepresence list`
or `kubectl describe pod <pod-name>`.

Depending on the type of intercept that gets created, the Traffic Agent will either route the incoming request to the
Traffic Manager so that it gets routed to a developer's workstation, or it will pass it along to the container in the
pod usually handling requests on that port.
