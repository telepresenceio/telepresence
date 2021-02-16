---
Description: "How Telepresence works to intercept traffic from your Kubernetes cluster to code running on your laptop."
---

# Architecture

## Traffic Manager

The Traffic Manager is the central point of communication between Traffic Agents in the cluster and Telepresence daemons on developer workstations, proxying all relevant inbound and outbound traffic and tracking active intercepts.  When Telepresence is run with either the `connect`, `intercept`, or `list` commands, the client first checks the cluster for the Traffic Manager deployment, and if missing it creates it.

## Traffic Agent

The Traffic Agent is a sidecar container that facilitates intercepts. When an intercept is started, the Traffic Agent container is injected into the deployment's pod(s). You can see the Traffic Agent's status by running `kubectl describe pod <pod-name>`.

## Changes from Service Preview

Using Ambassador's previous offering, Service Preview, the Traffic Agent had to be manually added to a pod by an annotation. This is no longer required as the Traffic Agent is automatically injected when an intercept is started.

For both the Traffic Manager and Traffic Agents, configuring Kubernetes ClusterRoles and ClusterRoleBindings is not required as it was in Service Preview. Instead, the user running Telepresence must already have sufficient permissions in the cluster to add and modify deployments in the cluster.
