---
description: "How Telepresence works to intercept traffic from your Kubernetes cluster to code running on your laptop."
---

# Telepresence Architecture

<div class="docs-diagram-wrapper">

![Telepresence Architecture](https://www.getambassador.io/images/documentation/telepresence-architecture.inline.svg)

</div>

## Telepresence CLI

The Telepresence CLI orchestrates the moving parts on the workstation: it starts the Telepresence Daemons,
authenticates against Ambassador Cloud, and then acts as a user-friendly interface to the Telepresence User Daemon.

## Telepresence Daemons
Telepresence has Daemons that run on a developer's workstation and act as the main point of communication to the cluster's
network in order to communicate with the cluster and handle intercepted traffic.

### User-Daemon
The User-Daemon coordinates the creation and deletion of intercepts by communicating with the [Traffic Manager](#traffic-manager).
All requests from and to the cluster go through this Daemon.

When you run telepresence login, Telepresence installs an enhanced version of the User-Daemon. This replaces the existing User-Daemon and
allows you to create intercepts on your local machine from Ambassador Cloud.

### Root-Daemon
The Root-Daemon manages the networking necessary to handle traffic between the local workstation and the cluster by setting up a
[Virtual Network Device](../tun-device) (VIF).  For a detailed description of how the VIF manages traffic and why it is necessary
please refer to this blog post:
[Implementing Telepresence Networking with a TUN Device](https://blog.getambassador.io/implementing-telepresence-networking-with-a-tun-device-a23a786d51e9).

When you run telepresence login, Telepresence installs an enhanced Telepresence User Daemon. This replaces the open source
User Daemon and allows you to create intercepts on your local machine from Ambassador Cloud.

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

## Ambassador Cloud

Ambassador Cloud enables Preview URLs by generating random ephemeral domain names and routing requests received on those
domains from authorized users to the appropriate Traffic Manager.

Ambassador Cloud also lets users manage their Preview URLs: making them publicly accessible, seeing users who have
accessed them and deleting them.

## Pod-Daemon

The Pod-Daemon is a modified version of the [Telepresence User-Daemon](#user-daemon) built as a container image so that
it can be inserted into a `Deployment` manifest as an additional container. This allows users to create intercepts completely
within the cluster with the benefit that the intercept stays active until the deployment with the Pod-Daemon container is removed.

The Pod-Daemon will take arguments and environment variables as part of the `Deployment` manifest to specify which service the intercept
should be run on and to provide similar configuration that would be provided when using Telepresence intercepts from the command line.

After being deployed to the cluster, it behaves similarly to the Telepresence User-Daemon and installs the [Traffic Agent Sidecar](#traffic-agent)
on the service that is being intercepted. After the intercept is created, traffic can then be redirected to the `Deployment` with the Pod-Daemon
container instead. The Pod-Daemon will automatically generate a Preview URL so that the intercept can be accessed from outside the cluster.
The Preview URL can be obtained from the Pod-Daemon logs if you are deploying it manually.

The Pod-Daemon was created for use as a component of Deployment Previews in order to automatically create intercepts with development images built
by CI so that changes from pull requests can be quickly visualized in a live cluster before changes are landed by accessing the Preview URL
link which would be posted to an associated GitHub pull request when using Deployment Previews.

See the [Deployment Previews quick-start](https://www.getambassador.io/docs/cloud/latest/deployment-previews/quick-start) for information on how to get started with Deployment Previews
or for a reference on how Pod-Daemon can be manually deployed to the cluster.


# Changes from Service Preview

Using Ambassador's previous offering, Service Preview, the Traffic Agent had to be manually added to a pod by an
annotation. This is no longer required as the Traffic Agent is automatically injected when an intercept is started.

Service Preview also started an intercept via `edgectl intercept`. The `edgectl` CLI is no longer required to intercept
as this functionality has been moved to the Telepresence CLI.

For both the Traffic Manager and Traffic Agents, configuring Kubernetes ClusterRoles and ClusterRoleBindings is not
required as it was in Service Preview. Instead, the user running Telepresence must already have sufficient permissions in the cluster to add and modify deployments in the cluster.
