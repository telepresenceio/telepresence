---
description: "How Telepresence works to intercept traffic from your Kubernetes cluster to code running on your laptop."
---

# Telepresence Architecture

<div class="docs-diagram-wrapper">

![Telepresence Architecture](../../../../../images/documentation/telepresence-architecture.inline.svg)

</div>

## Telepresence CLI

The Telepresence CLI orchestrates all the moving parts: it starts the Telepresence Daemon, installs the Traffic Manager
in your cluster, authenticates against Ambassador Cloud and configure all those elements to communicate with one
another.

## Telepresence Daemon

The Telepresence Daemon runs on a developer's workstation and is its main point of communication with the cluster's
network. All requests from and to the cluster go through the Daemon, which communicates with the Traffic Manager.

## Telepresence Pro Daemon
When you `telepresence login`, Telepresence recommends downloading the Telepresence Pro Daemon.
This replaces the open source User Daemon and provides additional features including:
* Creating intercepts on your local machine from Ambassador Cloud.

## Traffic Manager

The Traffic Manager is the central point of communication between Traffic Agents in the cluster and Telepresence Daemons
on developer workstations, proxying all relevant inbound and outbound traffic and tracking active intercepts. When
Telepresence is run with either the `connect`, `intercept`, or `list` commands, the Telepresence CLI first checks the
cluster for the Traffic Manager deployment, and if missing it creates it.

When an intercept gets created with a Preview URL, the Traffic Manager will establish a connection with Ambassador Cloud
so that Preview URL requests can be routed to the cluster. This allows Ambassador Cloud to reach the Traffic Manager
without requiring the Traffic Manager to be publicly exposed. Once the Traffic Manager receives a request from a Preview
URL, it forwards the request to the ingress service specified at the Preview URL creation.

## Traffic Agent

The Traffic Agent is a sidecar container that facilitates intercepts. When an intercept is started, the Traffic Agent
container is injected into the workload's pod(s). You can see the Traffic Agent's status by running `kubectl describe
pod <pod-name>`.

Depending on the type of intercept that gets created, the Traffic Agent will either route the incoming request to the
Traffic Manager so that it gets routed to a developer's workstation, or it will pass it along to the container in the
pod usually handling requests on that port.

## Ambassador Cloud

Ambassador Cloud enables Preview URLs by generating random ephemeral domain names and routing requests received on those
domains from authorized users to the appropriate Traffic Manager.

Ambassador Cloud also lets users manage their Preview URLs: making them publicly accessible, seeing users who have
accessed them and deleting them.

# Changes from Service Preview

Using Ambassador's previous offering, Service Preview, the Traffic Agent had to be manually added to a pod by an
annotation. This is no longer required as the Traffic Agent is automatically injected when an intercept is started.

Service Preview also started an intercept via `edgectl intercept`.  The `edgectl` CLI is no longer required to intercept
as this functionality has been moved to the Telepresence CLI.

For both the Traffic Manager and Traffic Agents, configuring Kubernetes ClusterRoles and ClusterRoleBindings is not
required as it was in Service Preview. Instead, the user running Telepresence must already have sufficient permissions in the cluster to add and modify deployments in the cluster.
