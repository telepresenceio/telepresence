# Connection Routing

## Outbound

### DNS resolution
When requesting a connection to a host, the IP of that host must be determined. Telepresence provides DNS resolvers to help with this task. There are currently three types of resolvers but only one of them will be used on a workstation at any given time. Common for all of them is that they will propagate a selection of the host lookups to be performed in the cluster. The selection normally includes all names ending with `.cluster.local` or a currently mapped namespace but more entries can be added to the list using the `include-suffixes` in the
[local DNS configuration](../config/#dns) 

#### Cluster side DNS lookups
The cluster side host lookup will be performed by the traffic-manager unless the client has an active intercept, in which case, the agent performing that intercept will be responsible for doing it. If the client has multiple intercepts, then all of them will be asked to perform the lookup, and the response to the client will contain the unique sum of IPs that they produce. It's therefore important to never have multiple intercepts that span more than one namespace[<sup>[1](#namespacelimit)</sup>]. The reason for asking all of them is that the workstation currently impersonates multiple containers, and it is not possible to determine on behalf of what container the lookup request is made.

#### MacOS resolver
This resolver hooks into the MacOS DNS system by creating files under `/etc/resolver`. Those files correspond to some domain and contain the port number of the Telepresence resolver. Telepresence creates one such file for each of the currently mapped namespaces and `include-suffixes`. The file `telepresence.local` contains a search path that is configured based on current intercepts so that single label names can be resolved correctly.

#### Linux systemd-resolved resolver
This resolver registers itself as part of telepresence's [VIF](../tun-device) using `systemd-resolved` and uses the DBus API to configure domains and routes that corresponds to the current set of intercepts and namespaces.

#### Linux overriding resolver
Linux systems that aren't configured with `systemd-resolved` will use this resolver. A Typical case is when running Telepresence [inside a docker container](../inside-container). During initialization, the resolver will first establish a _fallback_ connection to the IP passed as `--dns`, the one configured as `local-ip` in the [local DNS configuration](../config/#dns), or the primary `nameserver` registered in `/etc/resolv.conf`. It will then use iptables to actually override that IP so that requests to it instead ends up in the overriding resolver, which unless it succeeds on its own, will use the _fallback_.

### Routing

#### Subnets
The Telepresence `traffic-manager` service is responsible for discovering the cluster's Service subnet and all subnets used by the pods. In order to do this, it needs permission to create a dummy service[<sup>[2](#servicesubnet)</sup>] in its own namespace, and the ability to list, get, and watch nodes and pods. Some clusters will expose the pod subnets as `podCIDR` in the `Node` but some, like Amazon EKS, typically don't. Telepresence will then fall back to deriving the subnets from of all IPs of all pods.

The complete set of subnets that the [VIF](../tun-device) will be configured with is dynamic and may change during a connection's life cycle as new nodes arrive or disappear from the cluster. The set consists of what that the traffic-manager finds in the cluster, and the subnets configured using the [also-proxy](../config#alsoproxy) configuration option. Telepresence will remove subnets that are equal to, or completely covered by, other subnets.

#### Connection origin
A request to connect to an IP-address that belongs to one of the subnets of the [VIF](../tun-device) will cause a connection request to be made in the cluster. As with host name lookups, the request will originate from the traffic-manager unless the client has ongoing intercepts. If it does, one of the intercepted pods will be chosen, and the request will instead originate from that pod. This is a best-effort approach. Telepresence only knows that the request originated from the workstation. It cannot know that it is intended to originate from a specific pod when multiple intercepts are active.

There are multiple reasons for doing this. One is that it is important that the request originates from the correct namespace. Example:

```bash
curl some-host
```
results in a http request with header `Host: some-host`. Now, if a service-mesh like Istio performs header based routing, then it will fail to find that host unless the request originates from the same namespace as the host resides in. Another reason is that the configuration of a service mesh can contain very strict rules. If the request then originates from the wrong pod, it will be denied. Only one intercept at a time can be used if there is a need to ensure that the chosen pod is exactly right.

## Inbound

The traffic-manager and traffic-agent are mutually responsible for setting up the necessary connection to the workstation when an intercept becomes active. In versions prior to 2.3.2, this would be accomplished by the traffic-manager creating a port dynamically that it would pass to the traffic-agent. The traffic-agent would then forward the intercepted connection to that port, and the traffic-manager would forward it to the workstation. This lead to problems when integrating with service meshes like Istio since those dynamic ports needed to be configured. It also imposed an undesired requirement to be able to use mTLS between the traffic-manager and traffic-agent.

In 2.3.2, this changes, so that the traffic-agent instead creates a tunnel to the traffic-manager using the already existing gRPC API connection. The traffic-manager then forwards that using another tunnel to the workstation. This is completely invisible to other service meshes and is therefore much easier to configure.

##### Footnotes:
<p><a name="namespacelimit">1</a>: A future version of Telepresence will not allow concurrent intercepts that span multiple namespaces.</p>
<p><a name="servicesubnet">2</a>: The error message from an attempt to create a service in a bad subnet contains the service subnet. The trick of creating a dummy service is currently the only way to get Kubernetes to expose that subnet.</p>
