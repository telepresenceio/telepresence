---
description: "How Telepresence works under the hood to help with your Kubernetes development."
---

# Telepresence Technical Reference

## Architecture

### Traffic Manager

The Traffic Manager is the central point of communication between Traffic Agents in the cluster and Telepresence daemons on developer workstations, proxying all relevant inbound and outbound traffic and tracking active intercepts.  When Telepresence is run with either the `connect`, `intercept`, or `list` commands, the client first checks the cluster for the Traffic Manager deployment, and if missing it creates it.

### Traffic Agent

The Traffic Agent is a sidecar container that facilitates intercepts. When an intercept is started, the Traffic Agent container is injected into the deployment's pod(s). You can see the Traffic Agent's status by running `kubectl describe pod <pod-name>`.

## Changes from Service Preview

Using Ambassador's previous offering, Service Preview, the Traffic Agent had to be manually added to a pod by an annotation. This is no longer required as the Traffic Agent is automatically injected when an intercept is started.

For both the Traffic Manager and Traffic Agents, configuring Kubernetes ClusterRoles and ClusterRoleBindings is not required as it was in Service Preview. Instead, the user running Telepresence must already have sufficient permissions in the cluster to add and modify deployments in the cluster.

## Client Reference

The [Telepresence CLI client](../quick-start) is used to connect Telepresence to your cluster, start and stop intercepts, and create preview URLs. All commands are run in the form of `telepresence <command>`.

### Commands

A list of all CLI commands and flags is available by running `telepresence help`, but here is more detail on the most common ones.

| Command | Description |
| --- | --- |
| `connect` | Starts the local daemon and connects Telepresence to your cluster and installs the Traffic Manager if it is missing.  After connecting, outbound traffic is routed to the cluster so that you can interact with services as if your laptop was another pod (for example, curling a service by it's name) |
| `login` | Authenticates you to Ambassador Cloud to create, manage, and share [preview URLs](../howtos/preview-urls/)
| `logout` | Logs out out of Ambassador Cloud |
| `dashboard` | Reopens the Ambassador Cloud dashboard in your browser |
| `preview` | Create or remove preview domains for existing intercepts |
| `status` | Shows the current connectivity status |
| `quit` | Quits the local daemon, stopping all intercepts and outbound traffic to the cluster|
| `list` | Lists the current active intercepts |
| `intercept` | Intercepts a service, run followed by the service name to be intercepted and what port to proxy to your laptop: `telepresence intercept <svc name> --port <TCP port>`. This command can also start a process so you can run a local instance of the service you are intercepting. For example the following will intercept the hello service on port 8000 and start a Python web server: `telepresence intercept hello --port 8000 -- python3 -m http.server 8000` |
| `leave` | Stops an active intercept, for example: `telepresence leave hello` | 
| `uninstall` | Uninstalls Telepresence from your cluster, using the `--agent` flag to target the Traffic Agent for a specific deployment, the `--all-agents` flag to remove all Traffic Agents from all deployments, or the `--everything` flag to remove all Traffic Agents and the Traffic Manager.

