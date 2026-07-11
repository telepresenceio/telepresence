---
title: Connection Routing
toc_min_heading_level: 2
toc_max_heading_level: 2
---

# Connection Routing

## The Virtual Network Interface

The Telepresence daemon creates a Virtual Network Interface (VIF) when it connects to the cluster. The VIF routes the cluster's subnets so that every local tool can reach cluster services, and it intercepts DNS requests so that cluster names resolve (see [DNS resolution](#dns-resolution) below).

The VIF is a TUN-device, which means that it communicates with the workstation in terms of L3 IP-packets. The daemon recognizes UDP and TCP packets and tunnels their payload to the traffic-manager via its encrypted gRPC API. The traffic-manager then establishes the corresponding connections in the cluster. All protocol negotiation takes place in the client, because the VIF takes care of the L3 to L4 translation (i.e. the tunnel is L4, not L3). Both TCP and UDP are routed.

There is no need to tamper with firewalls in order to establish IP routes. The VIF makes the cluster subnets available during connect, and the kernel performs the routing automatically. When the session ends, the kernel is also responsible for cleaning up.

## DNS resolution

The Telepresence DNS resolver is dynamically configured to resolve names using the namespaces currently managed by the Traffic Manager. Processes running locally on the workstation have network access to all services in the currently connected namespace by service-name only, and to services in the other managed namespaces using `<service-name>.<namespace>`.

When requesting a connection to a host, the IP of that host must be determined. Telepresence provides DNS resolvers to help with this task. There are currently four types of resolvers but only one of them will be used on a workstation at any given time. Common for all of them is that they will propagate a selection of the host lookups to be performed in the cluster. The selection normally includes all names ending with `.cluster.local` or a currently mapped namespace but more entries can be added to the list using the `includeSuffixes` or `mappings` option in the
[cluster DNS configuration](config.md#dns) 

### Cluster side DNS lookups
The cluster side host lookup will be performed by a traffic-agent in the connected namespace, or by the traffic-manager if no such agent exists.

### macOS resolver
This resolver hooks into the macOS DNS system by creating files under `/etc/resolver`. Those files correspond to some domain and contain the port number of the Telepresence resolver. Telepresence creates one such file for each of the currently mapped namespaces and `include-suffixes` option. The file `telepresence.local` contains a search path that is configured based on currently connected namespace so that single label names can be resolved correctly.

### Linux systemd-resolved resolver
This resolver registers itself as part of telepresence's [VIF](#the-virtual-network-interface) using `systemd-resolved` and uses the DBus API to configure domains and routes that corresponds to the connected namespace and the namespaces managed by the Traffic Manager.

### Linux overriding resolver
Linux systems that aren't configured with `systemd-resolved` will use this resolver. A typical case is when running Telepresence [inside a docker container](../howtos/inside-container.md). During initialization, the resolver will first establish a _fallback_ connection to the IP passed as `--dns`, the one configured as `local-ip` in the [local DNS configuration](config.md#dns), or the primary `nameserver` registered in `/etc/resolv.conf`. It will then use iptables to actually override that IP so that requests to it instead end up in the overriding resolver, which unless it succeeds on its own, will use the _fallback_.

### Windows resolver
This resolver uses the DNS resolution capabilities of the [win-tun](https://www.wintun.net/) device in conjunction with [Win32_NetworkAdapterConfiguration SetDNSDomain](https://docs.microsoft.com/en-us/powershell/scripting/samples/performing-networking-tasks?view=powershell-7.2#assigning-the-dns-domain-for-a-network-adapter).

### Supported query types
The Telepresence DNS resolver is capable of resolving queries of type `A`, `AAAA`, `CNAME`, `MX`, `NS`, `PTR`, `SRV`, and `TXT`.

### DNS caching
The Telepresence DNS resolver often changes its configuration. Telepresence will not flush the host's DNS caches. Instead, all records will have a short Time To Live (TTL) so that such caches evict the entries quickly. This causes increased load on the Telepresence resolver (shorter TTL means more frequent queries) and to cater for that, telepresence now has an internal cache to minimize the number of DNS queries that it sends to the cluster. This cache is flushed as needed without causing instabilities.

## Routing

### Subnets
The Telepresence `traffic-manager` service is responsible for discovering the cluster's service subnet and all subnets used by the pods. In order to do this, it needs permission to create a dummy service[^1] in its own namespace, and the ability to list, get, and watch nodes and pods. Most clusters will expose the pod subnets as `podCIDR` in the `Node` while others, like Amazon EKS, don't. Telepresence will then fall back to deriving the subnets from the IPs of all pods. If you'd like to choose a specific method for discovering subnets, or want to provide the list yourself, you can use the `podCIDRStrategy` configuration value in the [helm](../install/manager.md) chart to do that.

The complete set of subnets that the [VIF](#the-virtual-network-interface) will be configured with is dynamic and may change during a connection's life cycle as new nodes arrive or disappear from the cluster. The set consists of what the traffic-manager finds in the cluster, and the subnets configured using the [also-proxy](config.md#alsoproxysubnets) configuration option. Telepresence will remove subnets that are equal to, or completely covered by, other subnets.

### Connection origin
A request to connect to an IP-address that belongs to one of the subnets of the [VIF](#the-virtual-network-interface) will cause a connection request to be made in the cluster. As with host name lookups, the request will originate from a traffic-agent in the connected namespace, or by the traffic-manager when no agent is present.

There are multiple reasons for doing this. One is that it is important that the request originates from the correct namespace. Example:

```bash
curl some-host
```
results in a http request with header `Host: some-host`. Now, if a service-mesh like Istio performs header-based routing, then it will fail to find that host unless the request originates from the same namespace as the host resides in. Another reason is that the configuration of a service mesh can contain very strict rules. If the request then originates from the wrong pod, it will be denied.

## Recursion detection
It is common that clusters used in development, such as Minikube, Minishift or k3s, run on the same host as the Telepresence client, often in a Docker container. Such clusters may have access to host network, which means that both DNS and L4 routing may be subjected to recursion.

### DNS recursion
When a local cluster's DNS-resolver fails to resolve a hostname, it may fall back to querying the local host network. This means that the Telepresence resolver will be asked to resolve a query that was issued from the cluster. Telepresence must check if such a query is recursive because there is a chance that it actually originated from the Telepresence DNS resolver and was dispatched to the `traffic-manager`, or a `traffic-agent`.

Telepresence handles this by sending one initial DNS-query to resolve the hostname "tel2-recursion-check.kube-system". If the cluster runs locally, and has access to the local host's network, then that query will recurse back into the Telepresence resolver. Telepresence remembers this and alters its own behavior so that queries that are believed to be recursions are detected and respond with an NXNAME record. Telepresence performs this solution to the best of its ability, but may not be completely accurate in all situations. There's a chance that the DNS-resolver will yield a false negative for the second query if the same hostname is queried more than once in rapid succession, that is when the second query is made before the first query has received a response from the cluster.

##### Footnotes:
[^1]: The error message from an attempt to create a service in a bad subnet contains the service subnet. The trick of creating a dummy service is currently the only way to get Kubernetes to expose that subnet.
