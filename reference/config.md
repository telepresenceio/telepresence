# Laptop-side configuration

Telepresence uses a `config.yml` file to store and change certain values.  The location of this file varies based on your OS:

* macOS: `$HOME/Library/Application Support/telepresence/config.yml`
* Linux: `$XDG_CONFIG_HOME/telepresence/config.yml` or, if that variable is not set, `$HOME/.config/telepresence/config.yml`

For Linux, the above paths are for a user-level configuration. For system-level configuration, use the file at `$XDG_CONFIG_DIRS/telepresence/config.yml` or, if that variable is empty, `/etc/xdg/telepresence/config.yml`.  If a file exists at both the user-level and system-level paths, the user-level path file will take precedence.

## Values

The config file currently supports values for the `timeouts` and `logLevels` keys.

Here is an example configuration:

```yaml
timeouts:
  agentInstall: 1m
  intercept: 10s
logLevels:
  userDaemon: debug
```

### Timeouts
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
|`trafficManagerAPI`|Waiting for connection to the gPRC API after `trafficManagerConnect` is successful|5 seconds|

### Log Levels
Values for `logLevels` are one of the following strings: `trace`, `debug`, `info`, `warning`, `error`, `fatal` and `panic`.
These are the valid fields for the `logLevels` key:

|Field|Description|Default|
|---|---|---|
|`userDaemon`|Logging level to be used by the User Daemon (logs to connector.log)|debug|
|`rootDaemon`|Logging level to be used for the Root Daemon (logs to daemon.log)|info|
