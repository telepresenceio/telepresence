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

The `client` config supports values for [cluster](#cluster), [dns](#dns), [grpc](#grpc), [images](#images), [logLevels](#log-levels), [routing](#routing),
and [timeouts](#timeouts).

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
    agentImage: tel2:$version$ # This overrides the agent image to inject when intercepting
  grpc:
    maxReceiveSize: 10Mi
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

| Field                     | Description                                                        | Type                                        | Default            |
|---------------------------|--------------------------------------------------------------------|---------------------------------------------|--------------------|
| `defaultManagerNamespace` | The default namespace where the Traffic Manager will be installed. | [string][yaml-str]                          | ambassador         |
| `mappedNamespaces`        | Namespaces that will be mapped by default.                         | [sequence][yaml-seq] of [strings][yaml-str] | `[]`               |
| `connectFromRootDaeamon`  | Make connections to the cluster directly from the root daemon.     | [boolean][yaml-bool]                        | `true`             |
| `agentPortForward`        | Let telepresence-client use port-forwards directly to agents       | [boolean][yaml-bool]                        | `true`             |
| `virtualIPSubnet`         | The CIDR to use when generating virtual IPs                        | [string][yaml-str]                          | platform dependent |

### DNS

The `client.dns` configuration offers options for configuring the DNS resolution behavior in a client application or system. Here is a summary of the available fields:

The fields for `client.dns` are: `localIP`, `excludeSuffixes`, `includeSuffixes`, and `lookupTimeout`.

| Field             | Description                                                                                                                                                         | Type                                        | Default                                            |
|-------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------|----------------------------------------------------|
| `localIP`         | The address of the local DNS server.  This entry is only used on Linux systems that are not configured to use systemd-resolved.                                     | IP address [string][yaml-str]               | first `nameserver` mentioned in `/etc/resolv.conf` |
| `excludeSuffixes` | Suffixes for which the DNS resolver will always fail (or fallback in case of the overriding resolver). Can be globally configured in the Helm chart.                | [sequence][yaml-seq] of [strings][yaml-str] | `[".arpa", ".com", ".io", ".net", ".org", ".ru"]`  |
| `includeSuffixes` | Suffixes for which the DNS resolver will always attempt to do a lookup.  Includes have higher priority than excludes. Can be globally configured in the Helm chart. | [sequence][yaml-seq] of [strings][yaml-str] | `[]`                                               |
| `excludes`        | Names to be excluded by the DNS resolver                                                                                                                            | `[]`                                        |
| `mappings`        | Names to be resolved to other names (CNAME records) or to explicit IP addresses                                                                                     | `[]`                                        |
| `lookupTimeout`   | Maximum time to wait for a cluster side host lookup.                                                                                                                | [duration][go-duration] [string][yaml-str]  | 4 seconds                                          |

Here is an example values.yaml:
```yaml
client:
  dns:
    includeSuffixes: [.private]
    excludeSuffixes: [.se, .com, .io, .net, .org, .ru]
    localIP: 8.8.8.8
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

### Grpc
The `maxReceiveSize` determines how large a message that the workstation receives via gRPC can be. The default is 4Mi (determined by gRPC). All traffic to and from the cluster is tunneled via gRPC.

The size is measured in bytes. You can express it as a plain integer or as a fixed-point number using E, G, M, or K. You can also use the power-of-two equivalents: Gi, Mi, Ki. For example, the following represent roughly the same value:
```
128974848, 129e6, 129M, 123Mi
```

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

The `intercept` controls applies to how Telepresence will intercept the communications to the intercepted service.

| Field                 | Description                                                                                                                                    | Type                | Default      |
|-----------------------|------------------------------------------------------------------------------------------------------------------------------------------------|---------------------|--------------|
| `defaultPort`         | controls which port is selected when no `--port` flag is given to the `telepresence intercept` command.                                        | int                 | 8080         |
| `useFtp`              | Use fuseftp instead of sshfs when mounting remote file systems                                                                                 | boolean             | false        |

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

| Field        | Description                                                         | Type                                        | Default |
|--------------|---------------------------------------------------------------------|---------------------------------------------|---------|
| `userDaemon` | Logging level to be used by the User Daemon (logs to connector.log) | [loglevel][logrus-level] [string][yaml-str] | debug   |
| `rootDaemon` | Logging level to be used for the Root Daemon (logs to daemon.log)   | [loglevel][logrus-level] [string][yaml-str] | info    |

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

### Timeouts

Values for `client.timeouts` are all durations either as a number of seconds
or as a string with a unit suffix of `ms`, `s`, `m`, or `h`.  Strings
can be fractional (`1.5h`) or combined (`2h45m`).

These are the valid fields for the `timeouts` key:

| Field                   | Description                                                                        | Type                                                                                                    | Default    |
|-------------------------|------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|------------|
| `agentInstall`          | Waiting for Traffic Agent to be installed                                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 2 minutes  |
| `apply`                 | Waiting for a Kubernetes manifest to be applied                                    | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 1 minute   |
| `clusterConnect`        | Waiting for cluster to be connected                                                | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 20 seconds |
| `connectivityCheck`     | Timeout used when checking if cluster is already proxied on the workstation        | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 500 ms     |
| `endpointDial`          | Waiting for a Dial to a service for which the IP is known                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 3 seconds  |
| `roundtripLatency`      | How much to add  to the endpointDial timeout when establishing a remote connection | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 2 seconds  |
| `intercept`             | Waiting for an intercept to become active                                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 30 seconds |
| `proxyDial`             | Waiting for an outbound connection to be established                               | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 5 seconds  |
| `trafficManagerConnect` | Waiting for the Traffic Manager API to connect for port forwards                   | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 60 seconds |
| `trafficManagerAPI`     | Waiting for connection to the gPRC API after `trafficManagerConnect` is successful | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 15 seconds |
| `helm`                  | Waiting for Helm operations (e.g. `install`) on the Traffic Manager                | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 30 seconds |

## Local Overrides

In addition, it is possible to override each of these variables at the local level by setting up new values in local config files.
There are two types of config values that can be set locally: those that apply to all clusters, which are set in a single `config.yml` file, and those
that only apply to specific clusters, which are set as extensions to the `$KUBECONFIG` file.

### Config for all clusters
Telepresence uses a `config.yml` file to store and change those configuration values that will be used for all clusters you use Telepresence with.
The location of this file varies based on your OS:

* macOS: `$HOME/Library/Application Support/telepresence/config.yml`
* Linux: `$XDG_CONFIG_HOME/telepresence/config.yml` or, if that variable is not set, `$HOME/.config/telepresence/config.yml`
* Windows: `%APPDATA%\telepresence\config.yml`

For Linux, the above paths are for a user-level configuration. For system-level configuration, use the file at `$XDG_CONFIG_DIRS/telepresence/config.yml` or, if that variable is empty, `/etc/xdg/telepresence/config.yml`.  If a file exists at both the user-level and system-level paths, the user-level path file will take precedence.

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
  agentImage: tel2:$version$ # This overrides the agent image to inject when intercepting
grpc:
  maxReceiveSize: 10Mi
```


## Workstation Per-Cluster Configuration

Configuration that is specific to a cluster can also be overriden per-workstation by modifying your `$KUBECONFIG` file.
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
[go-duration]: https://pkg.go.dev/time#ParseDuration
[logrus-level]: https://github.com/sirupsen/logrus/blob/v1.8.1/logrus.go#L25-L45
