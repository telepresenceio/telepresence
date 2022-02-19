# Laptop-side configuration

## Global Configuration
Telepresence uses a `config.yml` file to store and change certain global configuration values that will be used for all clusters you use Telepresence with.  The location of this file varies based on your OS:

* macOS: `$HOME/Library/Application Support/telepresence/config.yml`
* Linux: `$XDG_CONFIG_HOME/telepresence/config.yml` or, if that variable is not set, `$HOME/.config/telepresence/config.yml`
* Windows: `%APPDATA%\telepresence\config.yml`

For Linux, the above paths are for a user-level configuration. For system-level configuration, use the file at `$XDG_CONFIG_DIRS/telepresence/config.yml` or, if that variable is empty, `/etc/xdg/telepresence/config.yml`.  If a file exists at both the user-level and system-level paths, the user-level path file will take precedence.

### Values

The config file currently supports values for the `timeouts`, `logLevels`, `images`, `cloud`, and `grpc` keys.

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
  agentImage: ambassador-telepresence-agent:1.8.0 # This overrides the agent image to inject when intercepting
cloud:
  refreshMessages: 24h # Refresh messages from cloud every 24 hours instead of the default, which is 1 week.
grpc:
  maxReceiveSize: 10Mi
telepresenceAPI:
  port: 9980
```

#### Timeouts

Values for `timeouts` are all durations either as a number of seconds
or as a string with a unit suffix of `ms`, `s`, `m`, or `h`.  Strings
can be fractional (`1.5h`) or combined (`2h45m`).

These are the valid fields for the `timeouts` key:

| Field                   | Description                                                                        | Type                                                                                                    | Default    |
|-------------------------|------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|------------|
| `agentInstall`          | Waiting for Traffic Agent to be installed                                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 2 minutes  |
| `apply`                 | Waiting for a Kubernetes manifest to be applied                                    | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 1 minute   |
| `clusterConnect`        | Waiting for cluster to be connected                                                | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 20 seconds |
| `intercept`             | Waiting for an intercept to become active                                          | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 5 seconds  |
| `proxyDial`             | Waiting for an outbound connection to be established                               | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 5 seconds  |
| `trafficManagerConnect` | Waiting for the Traffic Manager API to connect for port fowards                    | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 20 seconds |
| `trafficManagerAPI`     | Waiting for connection to the gPRC API after `trafficManagerConnect` is successful | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 15 seconds |
| `helm`                  | Waiting for Helm operations (e.g. `install`) on the Traffic Manager                | [int][yaml-int] or [float][yaml-float] number of seconds, or [duration][go-duration] [string][yaml-str] | 2 minutes  |

#### Log Levels

Values for the `logLevels` fields are one of the following strings,
case insensitive:

 - `trace`
 - `debug`
 - `info`
 - `warning` or `warn`
 - `error`
 - `fatal`
 - `panic`

For whichever log-level you select, you will get logs labeled with that level and of higher severity.
(e.g. if you use `info`, you will also get logs labeled `error`. You will NOT get logs labeled `debug`.

These are the valid fields for the `logLevels` key:

| Field        | Description                                                         | Type                                        | Default |
|--------------|---------------------------------------------------------------------|---------------------------------------------|---------|
| `userDaemon` | Logging level to be used by the User Daemon (logs to connector.log) | [loglevel][logrus-level] [string][yaml-str] | debug   |
| `rootDaemon` | Logging level to be used for the Root Daemon (logs to daemon.log)   | [loglevel][logrus-level] [string][yaml-str] | info    |

#### Images
Values for `images` are strings. These values affect the objects that are deployed in the cluster,
so it's important to ensure users have the same configuration.

Additionally, you can deploy the server-side components with [Helm](../../install/helm), to prevent them
from being overridden by a client's config and use the [mutating-webhook](../cluster-config/#mutating-webhook)
to handle installation of the `traffic-agents`.

These are the valid fields for the `images` key:

| Field               | Description                                                                                                                                                                                                                                                                                                                                                                                    | Type                                               | Default              |
|---------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------|----------------------|
| `registry`          | Docker registry to be used for installing the Traffic Manager and default Traffic Agent.  If not using a helm chart to deploy server-side objects, changing this value will create a new traffic-manager deployment when using Telepresence commands.  Additionally, changing this value will update installed default `traffic-agents` to use the new registry when creating a new intercept. | Docker registry name [string][yaml-str]            | `docker.io/datawire` |
| `agentImage`        | `$registry/$imageName:$imageTag` to use when installing the Traffic Agent.  Changing this value will update pre-existing `traffic-agents` to use this new image.  *The `registry` value is not used for the `traffic-agent` if you have this value set.*                                                                                                                                       | qualified Docker image name [string][yaml-str]     | (unset)              |
| `webhookRegistry`   | The container `$registry` that the [Traffic Manager](../cluster-config/#mutating-webhook) will use with the `webhookAgentImage` *This value is only used if a new `traffic-manager` is deployed*                                                                                                                                                                                               | Docker registry name [string][yaml-str]            | `docker.io/datawire` |
| `webhookAgentImage` | The container image that the [Traffic Manager](../cluster-config/#mutating-webhook) will pull from the `webhookRegistry` when installing the Traffic Agent in annotated pods *This value is only used if a new `traffic-manager` is deployed*                                                                                                                                                  | non-qualified Docker image name [string][yaml-str] | (unset)              |

#### Cloud
Values for `cloud` are listed below and their type varies, so please see the chart for the expected type for each config value.
These fields control how the client interacts with the Cloud service.

| Field             | Description                                                                                                                                                                                                                                | Type                                       | Default |
|-------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------|---------|
| `skipLogin`       | Whether the CLI should skip automatic login to Ambassador Cloud.  If set to true, in order to perform personal intercepts you must have a [license key](../cluster-config/#air-gapped-cluster) installed in the cluster.                   | [bool][yaml-bool]                          | false   |
| `refreshMessages` | How frequently the CLI should communicate with Ambassador Cloud to get new command messages, which also resets whether the message has been raised or not. You will see each message at most once within the duration given by this config | [duration][go-duration] [string][yaml-str] | 168h    |
| `systemaHost`     | The host used to communicate with Ambassador Cloud                                                                                                                                                                                         | [string][yaml-str]         | app.getambassador.io    |
| `systemaPort`     | The port used with `systemaHost` to communicate with Ambassador Cloud                                                                                                                                                                      | [string][yaml-str]         | 443                     |

Telepresence attempts to auto-detect if the cluster is capable of
communication with Ambassador Cloud, but may still prompt you to log
in in cases where only the on-laptop client wishes to communicate with
Ambassador Cloud.  If you want those auto-login points to be disabled
as well, or would like it to not attempt to communicate with
Ambassador Cloud at all (even for the auto-detection), then be sure to
set the `skipLogin` value to `true`.

Reminder: To use personal intercepts, which normally require a login,
you must have a license key in your cluster and specify which
`agentImage` should be installed by also adding the following to your
`config.yml`:

```yaml
images:
  agentImage: <privateRegistry>/<agentImage>
```

#### Grpc
The `maxReceiveSize` determines how large a message that the workstation receives via gRPC can be. The default is 4Mi (determined by gRPC). All traffic to and from the cluster is tunneled via gRPC.

The size is measured in bytes. You can express it as a plain integer or as a fixed-point number using E, G, M, or K. You can also use the power-of-two equivalents: Gi, Mi, Ki. For example, the following represent roughly the same value:
```
128974848, 129e6, 129M, 123Mi
```

#### RESTful API server
The `telepresenceAPI` controls the behavior of Telepresence's RESTful API server that can be queried for additional information about ongoing intercepts. When present, and the `port` is set to a valid port number, it's propagated to the auto-installer so that application containers that can be intercepted gets the `TELEPRESENCE_API_PORT` environment set. The server can then be queried at `localhost:<TELEPRESENCE_API_PORT>`. In addition, the `traffic-agent` and the `user-daemon` on the workstation that performs an intercept will start the server on that port.
If the `traffic-manager` is auto-installed, its webhook agent injector will be configured to add the `TELEPRESENCE_API_PORT` environment to the app container when the `traffic-agent` is injected.
See [RESTful API server](../restapi) for more info.

#### Daemons

`daemons` controls which binary to use for the user daemon. By default it will
use the Telepresence binary. For example, this can be used to tell Telepresence to
use the Telepresence Pro binary.

| Field              | Description                                                 | Type               | Default                              |
|--------------------|-------------------------------------------------------------|--------------------|--------------------------------------|
| `userDaemonBinary` | The path to the binary you want to use for the User Daemon. | [string][yaml-str] | The path to Telepresence executable  |


## Per-Cluster Configuration
Some configuration is not global to Telepresence and is actually specific to a cluster.  Thus, we store that config information in your kubeconfig file, so that it is easier to maintain per-cluster configuration.

### Values
The current per-cluster configuration supports `dns`, `alsoProxy`, and `manager` keys.
To add configuration, simply add a `telepresence.io` entry to the cluster in your kubeconfig like so:

```
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        dns:
        also-proxy:
        manager:
  name: example-cluster
```
#### DNS
The fields for `dns` are: local-ip, remote-ip, exclude-suffixes, include-suffixes, and lookup-timeout.

| Field              | Description                                                                                                                     | Type                                        | Default                                                                     |
|--------------------|---------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------|-----------------------------------------------------------------------------|
| `local-ip`         | The address of the local DNS server.  This entry is only used on Linux systems that are not configured to use systemd-resolved. | IP address [string][yaml-str]               | first `nameserver` mentioned in `/etc/resolv.conf`                          |
| `remote-ip`        | The address of the cluster's DNS service.                                                                                       | IP address [string][yaml-str]               | IP of the `kube-dns.kube-system` or the `dns-default.openshift-dns` service |
| `exclude-suffixes` | Suffixes for which the DNS resolver will always fail (or fallback in case of the overriding resolver)                           | [sequence][yaml-seq] of [strings][yaml-str] | `[".arpa", ".com", ".io", ".net", ".org", ".ru"]`                           |
| `include-suffixes` | Suffixes for which the DNS resolver will always attempt to do a lookup.  Includes have higher priority than excludes.           | [sequence][yaml-seq] of [strings][yaml-str] | `[]`                                                                        |
| `lookup-timeout`   | Maximum time to wait for a cluster side host lookup.                                                                            | [duration][go-duration] [string][yaml-str]  | 4 seconds                                                                   |

Here is an example kubeconfig:
```
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        dns:
          include-suffixes:
          - .se
          exclude-suffixes:
          - .com
  name: example-cluster
```


#### AlsoProxy

When using `also-proxy`, you provide a list of subnets after the key in your kubeconfig file to be added to the TUN device.
All connections to addresses that the subnet spans will be dispatched to the cluster

Here is an example kubeconfig for the subnet `1.2.3.4/32`:
```
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        also-proxy:
        - 1.2.3.4/32
  name: example-cluster
```

#### NeverProxy

When using `never-proxy` you provide a list of subnets after the key in your kubeconfig file. These will never be routed via the
TUN device, even if they fall within the subnets (pod or service) for the cluster. Instead, whatever route they have before
telepresence connects is the route they will keep.

Here is an example kubeconfig for the subnet `1.2.3.4/32`:

```yaml
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        never-proxy:
        - 1.2.3.4/32
  name: example-cluster
```

##### Using AlsoProxy together with NeverProxy

Never proxy and also proxy are implemented as routing rules, meaning that when the two conflict, regular routing routes apply.
Usually this means that the most specific route will win.

So, for example, if an `also-proxy` subnet falls within a broader `never-proxy` subnet:

```yaml
never-proxy: [10.0.0.0/16]
also-proxy: [10.0.5.0/24]
```

Then the specific `also-proxy` of `10.0.5.0/24` will be proxied by the TUN device, whereas the rest of `10.0.0.0/16` will not.

Conversely if a `never-proxy` subnet is inside a larger `also-proxy` subnet:

```yaml
also-proxy: [10.0.0.0/16]
never-proxy: [10.0.5.0/24]
```

Then all of the also-proxy of `10.0.0.0/16` will be proxied, with the exception of the specific `never-proxy` of `10.0.5.0/24`

#### Manager

The `manager` key contains configuration for finding the `traffic-manager` that telepresence will connect to. It supports one key, `namespace`, indicating the namespace where the traffic manager is to be found

Here is an example kubeconfig that will instruct telepresence to connect to a manager in namespace `staging`:

```yaml
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1
    extensions:
    - name: telepresence.io
      extension:
        manager:
          namespace: staging
  name: example-cluster
```

[yaml-bool]: https://yaml.org/type/bool.html
[yaml-float]: https://yaml.org/type/float.html
[yaml-int]: https://yaml.org/type/int.html
[yaml-seq]: https://yaml.org/type/seq.html
[yaml-str]: https://yaml.org/type/str.html
[go-duration]: https://pkg.go.dev/time#ParseDuration
[logrus-level]: https://github.com/sirupsen/logrus/blob/v1.8.1/logrus.go#L25-L45
