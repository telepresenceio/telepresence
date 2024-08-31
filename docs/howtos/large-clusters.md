---
title: "Working with large clusters"
description: "Use Telepresence to intercept services in clusters with a large number of namespaces and workloads."
---
# Working with large clusters

## Large number of namespaces

### The problem
When telepresence connects to a cluster, it will configure the local DNS server so that each namespace in the cluster can be used as a top-level domain (TLD). E.g. if the cluster contains the namespace "example", then a curl for the name "my_service.example" will be directed to Telepresence DNS server, because it has announced that it wants to resolve the "example" domain.

Telepresence tries to be conservative about what namespaces that it will create TLDs for, and first check if the namespace is accessible by the user. This check can be time-consuming in a cluster with a large number of namespaces, because each check will typically take up to a second to complete, which means that for a cluster with 120 namespaces, this check can take two minutes. That's a long time to wait when doing `telepresence connect`.

### How to solve it

#### Limiting at connect

The `telepresence connect` command will accept the flag `--mapped-namespaces <comma separated names>`, which will limit the names that Telepresence create TLDs for in the DNS resolver. This may drastically decrease the time it takes to connect, and also improve the DNS resolver's performance.

#### Limiting the traffic-manager

It is possible to limit the namespaces that the traffic-manager will care about when it is installed or upgraded by passing the Helm chart value `managerRbac.namespaces`. This will tell the manager to only consider those namespaces with respect to intercepts and DNS. A manager configured with `managerRbac.namespaces` creates an implicit `mapped-namespaces` set for all clients that connect to it.

## Large number of pods

### The problem

A cluster with a large number of pods can be problematic in situations where the traffic-manager is unable to use its default behavior of retrieving the pod-subnets from the cluster nodes. The manager will then use a fallback method, which is to retrieve the IP of all pods and then use those IPs to calculate the pod-subnets. This in turn, might cause a very large number of requests to the Kubernetes API server.

### The solution

If it is RBAC permission limitations that prevent the traffic-manager from reading the `podCIDR` from the nodes, then adding the necessary permissions might help. But in many cases, the nodes will not have a `podCIDR` defined. The fallback for such cases is to specify the `podCIDRs` manually (and thus prevent the scan + calculation) using the Helm chart values:

```yaml
podCIDRStrategy: environment
podCIDRs:
  - <known podCIDR>
  ...
```

## Use a Namespaced Scoped Traffic Manager

Depending on use-case, it's sometimes beneficial to have several traffic-managers installed, each being responsible from a limited number of namespaces and prohibited from accessing other namespaces. A cluster can either have one single global traffic-manager, or one to many traffic-managers that are namespaced, but global and namespaced can never be combined.

A client that connects to a namespaced manager will automatically be limited to those namespaces.

See [Installing a namespaced-scoped traffic-manager](../install/manager#installing-a-namespace-scoped-traffic-manager) for details.
