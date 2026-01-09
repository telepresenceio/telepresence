
---
title: Laptop-side configuration
---
# Laptop-side configuration

There are a number of configuration values that can be tweaked to change how Telepresence behaves.
These can be set in three ways: globally, by a platform engineer with powers to deploy the Telepresence Traffic Manager, or locally by any user, either in the Telepresence configuration file `config.yml`, or as a Telepresence extension the Kubernetes configuration.
One important exception is the configuration of the of the traffic manager namespace, which, if it's different from the default of `ambassador`, [must be set](#manager) locally to be able to connect.

## Global Configuration

Global configuration is set at the Traffic Manager level and applies to any user connecting to that Traffic Manager.
To set it, simply pass in a `client` dictionary to the `telepresence helm install` command, with any config values you wish to set.

The `client` config supports values for [cluster](#cluster), [dns](#dns), [docker](#docker), [grpc](#grpc), [helm](#helm), [intercept](#intercept), [images](#images), [logLevels](#log-levels), [routing](#routing), and [timeouts](#timeouts).

Here is an example configuration to show you the conventions of how Telepresence is configured:
**note: This config shouldn't be used verbatim, since the registry `privateRepo` used doesn't exist**

```yaml
client:
  timeouts:
    agentInstall: 1m
    intercept: 10s
  logLevels:
    userDaemon: debug
  images:
    registry: privateRepo # This overrides the default docker.io/datawire repo
    agentImage: tel2:$version$ # This overrides the agent image to inject when engaging with a workload
  grpc:
    maxReceiveSize: 10Mi
    connectionTTL: 48h
  dns:
    includeSuffixes: [.private]
    excludeSuffixes: [.se, .com, .io, .net, .org, .ru]
    lookupTimeout: 30s
  routing:
    alsoProxySubnets:
      - 1.2.3.4/32
    neverProxySubnets:
      - 1.2.3.4/32
```

### Cluster
Values for `client.cluster` controls aspects on how client's connection to the traffic-manager.

| Field                     | Description                                                                           | Type                                        | Default         |
|---------------------------|---------------------------------------------------------------------------------------|---------------------------------------------|-----------------|
| `defaultManagerNamespace` | The default namespace where the Traffic Manager will be installed.                    | [string][yaml-str]                          | ambassador      |
| `mappedNamespaces`        | Namespaces that will be mapped by default.                                            | [sequence][yaml-seq] of [strings][yaml-str] | `[]`            |
| `connectFromRootDaeamon`  | Make connections to the cluster directly from the root daemon.                        | [boolean][yaml-bool]                        | `true`          |
| `agentPortForward`        | Let telepresence-client use port-forwards directly to agents                          | [boolean][yaml-bool]                        | `true`          |

### Docker
Values for the `client.docker` provides docker specific options.

| Field            | Description                                                                           | Type                  | Default         |
|------------------|---------------------------------------------------------------------------------------|-----------------------|-----------------|
| `addHostGateway` | Add `--add-host host.docker.internal:host-gateway` when starting the daemon in docker | [boolean][yaml-bool]  | `true` on linux |
| `telemount`      | Configuration of the image containing the telemount Docker volume plugin              | Image                 |                 |
| `teleroute`      | Configuration of the image containing the teleroute Docker network plugin             | Image                 |                 |
| `enableIPv4`     | Enable support for IPv4 networking                                                    | [boolean][yaml-bool]  | `true`          |
| `enableIPv6`     | Enable support for IPv6 networking                                                    | [boolean][yaml-bool]  | `true`          |

#### Image

The Image objects for `client.docker.telemount` and `client.docker.teleroute` provides information on how to download the
image from a registry.

The repositoryAPI must be capable of listing available tags if the tag is set to an empty string. The `ghcr.io/v2` API
does not support this for anonymous users, hence the default tags.

| Field         | Description                                      | Type               | Default               |
|---------------|--------------------------------------------------|--------------------|-----------------------|
| `registryAPI` | The URL used when connecting to the registry API | [string][yaml-str] | `ghcr.io/v2`          |
| `registry`    | The name of the registry                         | [string][yaml-str] | `ghcr.io`             |
| `namespace`   | The namespace of the component                   | [string][yaml-str] | `telepresenceio`      |
| `repository`  | The name of the component registry               | [string][yaml-str] | `telemount/teleroute` |
| `tag`         | The component tag                                | [string][yaml-str] | `0.1.6/0.3.0`         |

### DNS

The `client.dns` configuration offers options for configuring the DNS resolution behavior in a client application or system. Here is a summary of the available fields:

The fields for `client.dns` are: `localIP`, `excludeSuffixes`, `includeSuffixes`, and `lookupTimeout`.

| Field              | Description                                                                                                                                                         | Type                                        | Default                                            |
|--------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------|----------------------------------------------------|
| `localIP`          | The address of the local DNS server.  This entry is only used on Linux systems that are not configured to use systemd-resolved.                                     | IP address [string][yaml-str]               | first `nameserver` mentioned in `/etc/resolv.conf` |
| `excludeSuffixes`  | Suffixes for which the DNS resolver will always fail (or fallback in case of the overriding resolver). Can be globally configured in the Helm chart.                | [sequence][yaml-seq] of [strings][yaml-str] | `[".arpa", ".com", ".io", ".net", ".org", ".ru"]`  |
| `includeSuffixes`  | Suffixes for which the DNS resolver will always attempt to do a lookup.  Includes have higher priority than excludes. Can be globally configured in the Helm chart. | [sequence][yaml-seq] of [strings][yaml-str] | `[]`                                               |
| `excludes`         | Names to be excluded by the DNS resolver                                                                                                                            | `[]`                                        |                                                    |
| `mappings`         | Names to be resolved to other names (CNAME records) or to explicit IP addresses                                                                                     | `[]`                                        |                                                    |
| `lookupTimeout`    | Maximum time to wait for a cluster side host lookup.                                                                                                                | [duration][go-duration] [string][yaml-str]  | 4 seconds                                          |
| `recursionCheck`   | Enable DNS lookup recursion detection and avoidance.                                                                                                                | boolean                                     | false                                              |
| `useComplexLookup` | Disable use of simplified but efficient A and AAAA lookups.                                                                                                         | [boolean][yaml-bool]                        | `false`                                            |

Here is an example values.yaml:
```yaml
client:
  dns:
    includeSuffixes: [.private]
    excludeSuffixes: [.se, .com, .io, .net, .org, .ru]
    localAddress: 172.12.0.53
    lookupTimeout: 30s
```

#### Mappings

Allows you to map hostnames to aliases or to IP addresses. This is useful when you want to use an alternative name for a service in the cluster, or when you want the DNS resolver to map a name to an IP address of your choice.

In the given cluster, the service named `postgres` is located within a separate namespace titled `big-data`, and it's referred to as `psql` :

```yaml
dns:
  mappings:
    - name: postgres
      aliasFor: psql.big-data
    - name: my.own.domain
      aliasFor: 192.168.0.15
```

#### Exclude

Lists service names to be excluded from the Telepresence DNS server. This is useful when you want your application to interact with a local service instead of a cluster service. In this example, "redis" will not be resolved by the cluster, but locally.

```yaml
dns:
  excludes:
    - redis
```

#### recursionCheck

The `recursionCheck` is useful in situations when the cluster runs locally on the client and might have access to the client's DNS server. This is often the case when using clusters like Minikube or Kind, or have a Docker Desktop with Kubernetes enabled. The scenario is as follows:

1. A request to resolve a name arrives to the Telepresence DNS server.
2. Telepresence sends this request to the cluster's DNS server.
3. The cluster's DNS server is unable to resolve a name, and in an attempt to resolve it globally, it sends it to its host.
4. The host sends the request to the Telepresence DNS server.
5. Telepresence, already in progress of resolving this name, will wait for the previous attempt to succeed.
6. The original DNS request times out.

The final timeout is fairly slow. Enabling the `recursionCheck` will make it significantly faster because Telepresence will give up after a short timeout in step 5. The draw-back is that if multiple requests to resolve the same name arrive within a very short period of time, then there's a risk that Telepresence will not be able to distinguish the recursive calls from normal calls and respond with false timeouts.

### Helm
The `client.helm` object contains options for the `telepresence helm` commands

| Field       | Description                                                                                                  | Type               | Default                                         |
|-------------|--------------------------------------------------------------------------------------------------------------|--------------------|-------------------------------------------------|
| `chartURL`  | The URL used when downloading the Telepresence Helm chart when the version differs from the embedded version | [string][yaml-str] | `oci://ghcr.io/telepresenceio/telepresence-oss` |

### Grpc

All traffic to and from the cluster is tunneled via gRPC over a kubernetes port-forward connection.

| Field             | Description                                                                                   | Type                                      | Default  |
|-------------------|-----------------------------------------------------------------------------------------------|-------------------------------------------|----------|
| `connectionTTL`   | Max time that the traffic-manager or traffic-agent will keep an idle client connection alive  | [duration][go-duration]                   | `24h`    |
| `maxReceiveSize`  | Max size of a gRCP message.                                                                   | Docker registry name [quantity][quantity] | `4Mi`    |

### Images
Values for `client.images` are strings. These values affect the objects that are deployed in the cluster,
so it's important to ensure users have the same configuration.

These are the valid fields for the `client.images` key:

| Field         | Description                                                                              | Type                                           | Default                             |
|---------------|------------------------------------------------------------------------------------------|------------------------------------------------|-------------------------------------|
| `registry`    | Docker registry to be used for installing the Traffic Manager and default Traffic Agent. | Docker registry name [string][yaml-str]        | `docker.io/datawire`                |
| `agentImage`  | `$registry/$imageName:$imageTag` to use when installing the Traffic Agent.               | qualified Docker image name [string][yaml-str] | (unset)                             |
| `clientImage` | `$registry/$imageName:$imageTag` to use locally when connecting with `--docker`.         | qualified Docker image name [string][yaml-str] | `$registry/ambassador-telepresence` |

### Intercept

The `intercept` controls applies to how Telepresence will intercept the communications to replaced containers and intercepted services.

| Field         | Description                                                                                                           | Type                 | Default    |
|---------------|-----------------------------------------------------------------------------------------------------------------------|----------------------|------------|
| `defaultPort` | controls which port is selected when no `--port` flag is given to the `telepresence intercept` command                | [int][yaml-int]      | 8080       |
| `useFtp`      | Use fuseftp instead of sshfs when mounting remote file systems                                                        | [boolean][yaml-bool] | false      |
| `mountsRoot`  | Directory that will be used as the root for all automatically generated mount directories (not applicable on windows) | [string][yaml-str]   | env:TMPDIR |

### Log Levels

Values for the `client.logLevels` fields are one of the following strings,
case-insensitive:

- `trace`
- `debug`
- `info`
- `warning` or `warn`
- `error`

For whichever log-level you select, you will get logs labeled with that level and of higher severity.
(e.g. if you use `info`, you will also get logs labeled `error`. You will NOT get logs labeled `debug`.

These are the valid fields for the `client.logLevels` key:

| Field            | Description                                                                             | Type                                        | Default |
|------------------|-----------------------------------------------------------------------------------------|---------------------------------------------|---------|
| `userDaemon`     | Logging level to be used by the User Daemon (logs to connector.log)                     | [loglevel][slog-level] [string][yaml-str]   | info    |
| `rootDaemon`     | Logging level to be used for the Root Daemon (logs to daemon.log)                       | [loglevel][slog-level] [string][yaml-str] | info    |
| `kubeAuthDaemon` | Logging level to be used by the Kubernetes Authentication Daemon (logs to kubeauth.log) | [loglevel][slog-level] [string][yaml-str] | info    |
| `cli`            | Logging level to be used by the CLI frontend (logs to cli.log)                          | [loglevel][slog-level] [string][yaml-str] | info    |

### Routing

#### AlsoProxySubnets

When using `alsoProxySubnets`, you provide a list of subnets to be added to the TUN device.
All connections to addresses that the subnet spans will be dispatched to the cluster

Here is an example values.yaml for the subnet `1.2.3.4/32`:
```yaml
client:
  routing:
    alsoProxySubnets:
      - 1.2.3.4/32
```

#### NeverProxySubnets

When using `neverProxySubnets` you provide a list of subnets. These will never be routed via the TUN device,
even if they fall within the subnets (pod or service) for the cluster. Instead, whatever route they have before
telepresence connects is the route they will keep.

Here is an example kubeconfig for the subnet `1.2.3.4/32`:

```yaml
client:
  routing:
    neverProxySubnets:
      - 1.2.3.4/32
```

#### Using AlsoProxy together with NeverProxy

Never proxy and also proxy are implemented as routing rules, meaning that when the two conflict, regular routing routes apply.
Usually this means that the most specific route will win.

So, for example, if an `alsoProxySubnets` subnet falls within a broader `neverProxySubnets` subnet:

```yaml
neverProxySubnets: [10.0.0.0/16]
alsoProxySubnets: [10.0.5.0/24]
```

Then the specific `alsoProxySubnets` of `10.0.5.0/24` will be proxied by the TUN device, whereas the rest of `10.0.0.0/16` will not.

Conversely, if a `neverProxySubnets` subnet is inside a larger `alsoProxySubnets` subnet:

```yaml
alsoProxySubnets: [10.0.0.0/16]
neverProxySubnets: [10.0.5.0/24]
```

Then all of the `alsoProxySubnets` of `10.0.0.0/16` will be proxied, with the exception of the specific `neverProxySubnets` of `10.0.5.0/24`

These are the valid fields for the `client.routing` key:

| Field                     | Description                                                                            | Type                    | Default            |
|---------------------------|----------------------------------------------------------------------------------------|-------------------------|--------------------|
| `alsoProxySubnets`        | Proxy these subnets in addition to the service and pod subnets                         | [CIDR][cidr]            |                    |
| `neverProxySubnets`       | Do not proxy these subnets                                                             | [CIDR][cidr]            |                    |
| `allowConflictingSubnets` | Give Telepresence precedence when these subnets conflict with other network interfaces | [CIDR][cidr]            |                    |
| `recursionBlockDuration`  | Prevent recursion in VIF for this duration after a connect                             | [duration][go-duration] |                    |
| `virtualSubnet`           | The CIDR to use when generating virtual IPs                                            | [CIDR][cidr]            | platform dependent |
| `autoResolveConflicts`    | Auto resolve conflicts using a virtual subnet                                          | [bool][yaml-bool]       | true               |


### Timeouts

Values for `client.timeouts` are all durations either as a number of seconds
or as a string with a unit suffix of `ms`, `s`, `m`, or `h`.  Strings
can be fractional (`1.5h`) or combined (`2h45m`).

These are the valid fields for the `timeouts` key:

| Field                   | Description                                                                        | Type                                                                                                    | Default         |
|-------------------------|------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|-----------------|
| `agentInstall`          | Waiting for Traffic Agent to be installed                                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 2 minutes       |
| `apply`                 | Waiting for a Kubernetes manifest to be applied                                    | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 1 minute        |
| `clusterConnect`        | Waiting for cluster to be connected                                                | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 20 seconds      |
| `connectivityCheck`     | Timeout used when checking if cluster is already proxied on the workstation        | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 500 ms (max 5s) |
| `endpointDial`          | Waiting for a Dial to a service for which the IP is known                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 3 seconds       |
| `roundtripLatency`      | How much to add  to the endpointDial timeout when establishing a remote connection | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 2 seconds       |
| `intercept`             | Waiting for an intercept to become active                                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 30 seconds      |
| `proxyDial`             | Waiting for an outbound connection to be established                               | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 5 seconds       |
| `trafficManagerConnect` | Waiting for the Traffic Manager API to connect for port forwards                   | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 60 seconds      |
| `trafficManagerAPI`     | Waiting for connection to the gPRC API after `trafficManagerConnect` is successful | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 15 seconds      |
| `helm`                  | Waiting for Helm operations (e.g. `install`) on the Traffic Manager                | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 30 seconds      |

## Local Overrides

In addition, it is possible to override each of these variables at the local level by setting up new values in local config files.
There are two types of config values that can be set locally: those that apply to all clusters, which are set in a single `config.yml` file, and those
that only apply to specific clusters, which are set as extensions to the `$KUBECONFIG` file.

### Client Config
Telepresence uses a `config.yml` file to store and change those configuration values that will be used by the Telepresence client.
The location of this file varies based on your OS:

* macOS: `$HOME/Library/Application Support/telepresence/config.yml`
* Linux: `$XDG_CONFIG_HOME/telepresence/config.yml` or, if that variable is not set, `$HOME/.config/telepresence/config.yml`
* Windows: `%APPDATA%\telepresence\config.yml`

### Values

The definitions of the values in the `config.yml` are identical to those values in the `client` config above, but without the top level `client` key.

Here is an example configuration to show you the conventions of how Telepresence is configured:
**note: This config shouldn't be used verbatim, since the registry `privateRepo` used doesn't exist**

```yaml
timeouts:
  agentInstall: 1m
  intercept: 10s
logLevels:
  userDaemon: debug
images:
  registry: privateRepo # This overrides the default docker.io/datawire repo
  agentImage: tel2:$version$ # This overrides the agent image to inject when engaging with a workload
grpc:
  maxReceiveSize: 10Mi
```


## Workstation Per-Cluster Configuration

Configuration that is specific to a cluster connection can also be overriden per-workstation by modifying your `$KUBECONFIG` file.
It is recommended that you do not do this, and instead rely on upstream values provided to the Traffic Manager. This ensures
that all users that connect to the Traffic Manager will behave the same.
An important exception to this is the [`cluster.defaultManagerNamespace` configuration](#manager) which must be set locally.

### Values

The definitions of the values in the Telepresence kubeconfig extension are identical to those values in the `config.yml` config. The values will be merged into the config and have higher
priority when Telepresence is connected to the extended cluster.

Example kubeconfig:
```yaml
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        cluster:
          defaultManagerNamespace: staging
        dns:
          includeSuffixes: [.private]
          excludeSuffixes: [.se, .com, .io, .net, .org, .ru]
        routing:
          neverProxy: [10.0.0.0/16]
          alsoProxy: [10.0.5.0/24]
  name: example-cluster
```

#### Manager

This is the one cluster configuration that cannot be set using the Helm chart because it defines how Telepresence  connects to
the Traffic manager. When not default, that setting needs to be configured in the workstation's kubeconfig for the cluster.

The `cluster.defaultManagerNamespace` key contains configuration for finding the `traffic-manager` that telepresence will connect to.

Here is an example kubeconfig that will instruct telepresence to connect to a manager in namespace `staging`. The setting can be overridden using the Telepresence connect flag `--manager-namespace`.

Please note that the `cluster.defaultManagerNamespace` can be set in the `config.yml` too, but will then not be unique per cluster.

```yaml
apiVersion: v1
clusters:
  - cluster:
      server: https://127.0.0.1
      extensions:
        - name: telepresence.io
          extension:
            cluster:
              defaultManagerNamespace: staging
    name: example-cluster
```

[yaml-bool]: https://yaml.org/type/bool.html
[yaml-float]: https://yaml.org/type/float.html
[yaml-int]: https://yaml.org/type/int.html
[yaml-seq]: https://yaml.org/type/seq.html
[yaml-str]: https://yaml.org/type/str.html
[quantity]: ../common/quantity.md
[go-duration]: https://pkg.go.dev/time#ParseDuration
[slog-level]: https://pkg.go.dev/log/slog#Level
[cidr]: https://www.geeksforgeeks.org/classless-inter-domain-routing-cidr/
