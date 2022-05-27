# Connection Routing

## Outbound

### DNS resolution
When requesting a connection to a host, the IP of that host must be determined. Telepresence provides DNS resolvers to help with this task. There are currently four types of resolvers but only one of them will be used on a workstation at any given time. Common for all of them is that they will propagate a selection of the host lookups to be performed in the cluster. The selection normally includes all names ending with `.cluster.local` or a currently mapped namespace but more entries can be added to the list using the `include-suffixes` option in the
[local DNS configuration](../config/#dns) 

#### Cluster side DNS lookups
The cluster side host lookup will be performed by the traffic-manager unless the client has an active intercept, in which case, the agent performing that intercept will be responsible for doing it. If the client has multiple intercepts, then all of them will be asked to perform the lookup, and the response to the client will contain the unique sum of IPs that they produce. It's therefore important to never have multiple intercepts that span more than one namespace[<sup>[1](#namespacelimit)</sup>]. The reason for asking all of them is that the workstation currently impersonates multiple containers, and it is not possible to determine on behalf of what container the lookup request is made.

#### macOS resolver
This resolver hooks into the macOS DNS system by creating files under `/etc/resolver`. Those files correspond to some domain and contain the port number of the Telepresence resolver. Telepresence creates one such file for each of the currently mapped namespaces and `include-suffixes` option. The file `telepresence.local` contains a search path that is configured based on current intercepts so that single label names can be resolved correctly.

#### Linux systemd-resolved resolver
This resolver registers itself as part of telepresence's [VIF](../tun-device) using `systemd-resolved` and uses the DBus API to configure domains and routes that corresponds to the current set of intercepts and namespaces.

#### Linux overriding resolver
Linux systems that aren't configured with `systemd-resolved` will use this resolver. A Typical case is when running Telepresence [inside a docker container](../inside-container). During initialization, the resolver will first establish a _fallback_ connection to the IP passed as `--dns`, the one configured as `local-ip` in the [local DNS configuration](../config/#dns), or the primary `nameserver` registered in `/etc/resolv.conf`. It will then use iptables to actually override that IP so that requests to it instead end up in the overriding resolver, which unless it succeeds on its own, will use the _fallback_.

#### Windows resolver
This resolver uses the DNS resolution capabilities of the [win-tun](https://www.wintun.net/) device in conjunction with [Win32_NetworkAdapterConfiguration SetDNSDomain](https://docs.microsoft.com/en-us/powershell/scripting/samples/performing-networking-tasks?view=powershell-7.2#assigning-the-dns-domain-for-a-network-adapter).

#### DNS caching
The Telepresence DNS resolver often changes its configuration. This means that Telepresence must either flush the DNS caches on the local host, or ensure that DNS-records returned from the Telepresence resolver aren't cached (or cached for a very short time). All operating systems have different ways of flushing the DNS caches and even different versions of one system may have differences. Also, on some systems it is necessary to actually kill and restart processes to ensure a proper flush, which in turn may result in network instabilities.

Starting with 2.4.7, Telepresence will no longer flush the host's DNS caches. Instead, all records will have a short Time To Live (TTL) so that such caches evict the entries quickly. This causes increased load on the Telepresence resolver (shorter TTL means more frequent queries) and to cater for that, telepresence now has an internal cache to minimize the number of DNS queries that it sends to the cluster. This cache is flushed as needed without causing instabilities.

### Routing

#### Subnets
The Telepresence `traffic-manager` service is responsible for discovering the cluster's service subnet and all subnets used by the pods. In order to do this, it needs permission to create a dummy service[<sup>[2](#servicesubnet)</sup>] in its own namespace, and the ability to list, get, and watch nodes and pods. Most clusters will expose the pod subnets as `podCIDR` in the `Node` while others, like Amazon EKS, don't. Telepresence will then fall back to deriving the subnets from the IPs of all pods. If you'd like to choose a specific method for discovering subnets, or want to provide the list yourself, you can use the `podCIDRStrategy` configuration value in the [helm](../../install/helm) chart to do that.

The complete set of subnets that the [VIF](../tun-device) will be configured with is dynamic and may change during a connection's life cycle as new nodes arrive or disappear from the cluster. The set consists of what that the traffic-manager finds in the cluster, and the subnets configured using the [also-proxy](../config#alsoproxy) configuration option. Telepresence will remove subnets that are equal to, or completely covered by, other subnets.

#### Connection origin
A request to connect to an IP-address that belongs to one of the subnets of the [VIF](../tun-device) will cause a connection request to be made in the cluster. As with host name lookups, the request will originate from the traffic-manager unless the client has ongoing intercepts. If it does, one of the intercepted pods will be chosen, and the request will instead originate from that pod. This is a best-effort approach. Telepresence only knows that the request originated from the workstation. It cannot know that it is intended to originate from a specific pod when multiple intercepts are active.

A `--local-only` intercept will not have any effect on the connection origin because there is no pod from which the connection can originate. The intercept must be made on a workload that has been deployed in the cluster if there's a requirement for correct connection origin.

There are multiple reasons for doing this. One is that it is important that the request originates from the correct namespace. Example:

```bash
curl some-host
```
results in a http request with header `Host: some-host`. Now, if a service-mesh like Istio performs header based routing, then it will fail to find that host unless the request originates from the same namespace as the host resides in. Another reason is that the configuration of a service mesh can contain very strict rules. If the request then originates from the wrong pod, it will be denied. Only one intercept at a time can be used if there is a need to ensure that the chosen pod is exactly right.

### Recursion detection
It is common that clusters used in development, such as Minikube, Minishift or k3s, run on the same host as the Telepresence client, often in a Docker container. Such clusters may have access to host network, which means that both DNS and L4 routing may be subjected to recursion.

#### DNS recursion
When a local cluster's DNS-resolver fails to resolve a hostname, it may fall back to querying the local host network. This means that the Telepresence resolver will be asked to resolve a query that was issued from the cluster. Telepresence must check if such a query is recursive because there is a chance that it actually originated from the Telepresence DNS resolver and was dispatched to the `traffic-manager`, or a `traffic-agent`.

Telepresence handles this by sending one initial DNS-query to resolve the hostname "tel2-recursion-check.kube-system". If the cluster runs locally, and has access to the local host's network, then that query will recurse back into the Telepresence resolver. Telepresence remembers this and alters its own behavior so that queries that are believed to be recursions are detected and respond with an NXNAME record. Telepresence performs this solution to the best of its ability, but may not be completely accurate in all situations. There's a chance that the DNS-resolver will yield a false negative for the second query if the same hostname is queried more than once in rapid succession, that is when the second query is made before the first query has received a response from the cluster.

#### Connect recursion
A cluster running locally may dispatch connection attempts to non-existing host:port combinations to the host network. This means that they may reach the Telepresence [VIF](../tun-device). Endless recursions occur if the VIF simply dispatches such attempts on to the cluster.

The telepresence client handles this by serializing all connection attempts to one specific IP:PORT, trapping all subsequent attempts to connect to that IP:PORT until the first attempt has completed. If the first attempt was deemed a success, then the currently trapped attempts are allowed to proceed. If the first attempt failed, then the currently trapped attempts fail.

## Inbound

The traffic-manager and traffic-agent are mutually responsible for setting up the necessary connection to the workstation when an intercept becomes active. In versions prior to 2.3.2, this would be accomplished by the traffic-manager creating a port dynamically that it would pass to the traffic-agent. The traffic-agent would then forward the intercepted connection to that port, and the traffic-manager would forward it to the workstation. This lead to problems when integrating with service meshes like Istio since those dynamic ports needed to be configured. It also imposed an undesired requirement to be able to use mTLS between the traffic-manager and traffic-agent.

In 2.3.2, this changes, so that the traffic-agent instead creates a tunnel to the traffic-manager using the already existing gRPC API connection. The traffic-manager then forwards that using another tunnel to the workstation. This is completely invisible to other service meshes and is therefore much easier to configure.

##### Footnotes:
<p><a name="namespacelimit">1</a>: A future version of Telepresence will not allow concurrent intercepts that span multiple namespaces.</p>
<p><a name="servicesubnet">2</a>: The error message from an attempt to create a service in a bad subnet contains the service subnet. The trick of creating a dummy service is currently the only way to get Kubernetes to expose that subnet.</p>
