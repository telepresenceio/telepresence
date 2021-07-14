# Laptop-side configuration

## Global Configuration
Telepresence uses a `config.yml` file to store and change certain global configuration values that will be used for all clusters you use Telepresence with.  The location of this file varies based on your OS:

* macOS: `$HOME/Library/Application Support/telepresence/config.yml`
* Linux: `$XDG_CONFIG_HOME/telepresence/config.yml` or, if that variable is not set, `$HOME/.config/telepresence/config.yml`

For Linux, the above paths are for a user-level configuration. For system-level configuration, use the file at `$XDG_CONFIG_DIRS/telepresence/config.yml` or, if that variable is empty, `/etc/xdg/telepresence/config.yml`.  If a file exists at both the user-level and system-level paths, the user-level path file will take precedence.

### Values

The config file currently supports values for the `timeouts`, `logLevels`, `images` keys.

Here is an example configuration:

```yaml
timeouts:
  agentInstall: 1m
  intercept: 10s
logLevels:
  userDaemon: debug
images:
  registry: privateRepo
  agentImage: ambassador-telepresence-agent:1.8.0
```

#### Timeouts
Values for `timeouts` are all durations either as a number respresenting seconds or a string with a unit suffix of `ms`, `s`, `m`, or `h`.  Strings can be fractional (`1.5h`) or combined (`2h45m`).

These are the valid fields for the `timeouts` key:

|Field|Description|Default|
|---|---|---|
|`agentInstall`|Waiting for Traffic Agent to be installed|2 minutes|
|`apply`|Waiting for a Kubernetes manifest to be applied|1 minute|
|`clusterConnect`|Waiting for cluster to be connected|20 seconds|
|`intercept`|Waiting for an intercept to become active|5 seconds|
|`proxyDial`|Waiting for an outbound connection to be established|5 seconds|
|`trafficManagerConnect`|Waiting for the Traffic Manager API to connect for port fowards|20 seconds|
|`trafficManagerAPI`|Waiting for connection to the gPRC API after `trafficManagerConnect` is successful|15 seconds|

#### Log Levels
Values for `logLevels` are one of the following strings: `trace`, `debug`, `info`, `warning`, `error`, `fatal` and `panic`.
These are the valid fields for the `logLevels` key:

|Field|Description|Default|
|---|---|---|
|`userDaemon`|Logging level to be used by the User Daemon (logs to connector.log)|debug|
|`rootDaemon`|Logging level to be used for the Root Daemon (logs to daemon.log)|info|

#### Images
Values for `images` are strings. These values affect the objects that are deployed in the cluster,
so it's important to ensure users have the same configuration.

Additionally, you can deploy the server-side components with [Helm](../../install/helm), to prevent them
from being overridden by a client's config and use the [mutating-webhook](../clusterpconfig/#mutating-webhook)
to handle installation of the `traffic-agents`.

These are the valid fields for the `images` key:

|Field|Description|Default|
|---|---|---|
|`registry`|Docker registry to be used for installing the Traffic Manager and default Traffic Agent. If not using a helm chart to deploy server-side objects, changing this value will create a new traffic-manager deployment when using Telepresence commands. Additionally, changing this value will update installed default `traffic-agents` to use the new registry when creating a new intercept.|docker.io/datawire|
|`agentImage`|$registry/$imageName:$imageTag to use when installing the Traffic Agent. Changing this value will update pre-existing `traffic-agents` to use this new image. * the `registry` value is not used for the `traffic-agent` if you have this value set *||
|`webhookRegistry`|The container $registry that the [Traffic Manager](../cluster-config/#mutating-webhook) will use with the `webhookAgentImage` *This value is only used if a new traffic-manager is deployed*||
|`webhookAgentImage`|The container image that the [Traffic Manager](../cluster-config/#mutating-webhook) will use when installing the Traffic Agent in annotated pods *This value is only used if a new traffic-manager is deployed*||

#### Cloud
These fields control how the client interacts with the Cloud service.
Currently there is only one key and it accepts bools: `1`, `t`, `T`, `TRUE`, `true`, `True`, `0`, `f`, `F,` `FALSE`

|Field|Description|Default|
|---|---|---|
|`skipLogin`|Whether the cli should skipping login to Ambassador Cloud. If you set to true, you must have a [license](../cluster-config/#air-gapped-cluster) installed in the cluster to perform selective intercepts |false|


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

|Field|Description|Type|Default|
|---|---|---|---|
|`local-ip`|The address of the local DNS server. This entry is only used on Linux system that are not configured to use systemd.resolved|ip|first line of /etc/resolv.conf|
|`remote-ip`|the address of the cluster's DNS service|ip|IP of the kube-dns.kube-system or the dns-default.openshift-dns service|
|`exclude-suffixes`|suffixes for which the DNS resolver will always fail (or fallback in case of the overriding resolver)|list||
|`include-suffixes`|suffixes for which the DNS resolver will always attempt to do a lookup. Includes have higher priority than excludes.|list||
|`lookup-timeout`|maximum time to wait for a cluster side host lookup|duration||

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
When using `also-proxy`, you provide a list of subnets after the key in your kubeconfig file to be added to the TUN device. All connections to addresses that the subnet spans will be dispatched to the cluster

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
