---
title: "Telepresence vs mirrord"
hide_table_of_contents: true
---

## Telepresence

Telepresence is a very feature rich tool, designed to handle a large majority of use-cases. You can use it as a cluster VPN only, or use one of its three different ways (replace, intercept, or ingest) to engage with the cluster's resources.

Telepresence is intended to be installed in the cluster by an administrator and then let clients connect with a very limited set of permissions. This model is generally required by larger companies.

The client can be either completely contained in Docker or run directly on the workstation. The latter requires the creation of a virtual network device, but admin access is not needed when Telepresence is installed using a package installer that configures the root daemon as a system service.

## mirrord

Mirrord was designed with simplicity in mind. You install the CLI tool, and that's it. It will do the rest automatically under the hood.

Mirrord solves the same problem as Telepresence, but in a different way. Instead of providing a proper network
device and remotely mounted filesystems, mirrord will link the client application with a `mirrord-layer` shared library. This library will inject code that intercepts accesses to the network, file system, and environment variables, and reroute them to a corresponding process in the cluster (the `mirrord-agent`) which then interacts with the targeted pod.

### Limitations with Code Injection

Telepresence 1.x used the [code injection approach](https://www.getambassador.io/blog/code-injection-on-linux-and-macos), but desided to abandon it due to several limitations:

1. It will only work on Linux and macOS platforms. There's no native support on Windows.
2. It will only work with dynamically linked executables.
3. It cannot be used with docker unless you rebuild the container and inject the `mirrord-layer` into it.
4. `DYLD_INSERT_LIBRARIES` causes various problems on macOS (SIP prevents it from being used), especially on silicon-based machines where mirrord will require Rosetta.
5. Should Apple decide to protect their intel-based platform the same way as the silicon-based one in a future release, then mirrord will likely be problematic to use on that platform.

### Cluster Permissions

Mirrord does not require a sidecar. Instead they install a the `mirror-agent` into the namespace of the pod that it impersonates. That agent requires several permissions that a cluster admin might consider a security risk:

* `CAP_NET_ADMIN` and `CAP_NET_RAW` - required for modifying routing tables
* `CAP_SYS_PTRACE` - required for reading target pod environment
* `CAP_SYS_ADMIN` - required for joining target pod network namespace

Unless using "mirrord for Teams" (proprietary), all users must have permissions to create the job running the `mirror-agent` in the cluster.

## Comparison Telepresence vs mirrord

This comparison chart applies to the Open Source editions of both products.

| Feature                                                                      | Telepresence | mirrord |
|------------------------------------------------------------------------------|--------------|---------|
| Run or Debug your cluster containers locally                                 | ✅            | ✅       |
| Does not need administrative permission on workstation                       | ✅ [^1]       | ✅       |
| Can be used with very large clusters                                         | ✅            | ✅       |
| Works without interrupting the remote service                                | ✅ [^2]       | ✅       |
| Doesn't require injection of a sidecar                                       | ✅ [^3]       | ✅       |
| Supports connecting to clusters over a corporate VPN                         | ✅            | ✅       |
| Can intercept traffic                                                        | ✅            | ✅       |
| Can filter traffic based on HTTP headers and paths                           | ✅            | ✅       |
| Can mirror traffic                                                           | ✅            | ✅       |
| Can ingest a container                                                       | ✅            | ❌       |
| Can replace a container                                                      | ✅            | ❌       |
| Can act as a cluster VPN only                                                | ✅            | ❌       |
| Will work with statically linked binaries                                    | ✅            | ❌       |
| Runs natively on windows                                                     | ✅            | ❌       |
| Can intercept traffic to and from pod's localhost                            | ✅            | ❌       |
| Remotely mounted file system available from all applications                 | ✅            | ❌       |
| Cluster network available to all applications (including browser)            | ✅            | ❌       |
| Can run the same docker container locally without rebuilding it              | ✅            | ❌       |
| Integrates with Docker Compose                                               | ✅            | ❌       |
| Provides an API server allowing introspection of replacements and intercepts | ✅            | ❌       |
| Provides remote mounts as volumes in docker                                  | ✅            | ❌       |
| Does not require special capabilities such as CAP_SYS_ADMIN in the cluster   | ✅            | ❌       |
| Centralized client configuration using Helm chart                            | ✅            | ❌       |
| Installed using a JSON-schema validated Helm chart                           | ✅            | ❌       |
| Client need no special RBAC permissions                                      | ✅            | ❌       |

[^1]: Telepresence does not require root access on the workstation when installed using a package installer (which configures the root daemon as a system service) or when running in docker mode.

[^2]: The remote service will only restart when a traffic-agent sidecar is installed. Pod disruption budgets or pre-installed agents can be used to avoid interruptions.

[^3]: A traffic-agent is necessary when engaging with a pod. It is unnecessary when using Telepresence as a VPN.
