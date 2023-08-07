# Changelog

### 2.13.3 (May 25, 2023)

- Feature: Add `.Values.hooks.curl.imagePullSecrets` and `.Values.hooks.curl.imagePullSecrets` to Helm values.
  PR [3079](https://github.com/telepresenceio/telepresence/pull/3079).

- Change: The default setting of the reinvocationPolicy for the mutating webhook dealing with agent injections
  changed from `Never` to `IfNeeded`.

- Bugfix: The `eks.amazonaws.com/serviceaccount` volume injected by EKS is now exported and remotely mounted
  during an intercept.
  Ticket [3166](https://github.com/telepresenceio/telepresence/issues/3166).

- Bugfix: The mutating webhook now correctly applies the namespace selector even if the cluster version contains
  non-numeric characters. For example, it can now handle versions such as Major:"1", Minor:"22+".
  PR [3184](https://github.com/telepresenceio/telepresence/pull/3184).

- Bugfix: The "telepresence" Docker network will now propagate DNS AAAA queries to the Telepresence DNS resolver when
  it runs in a Docker container.
  Ticket [3179](https://github.com/telepresenceio/telepresence/issues/3179).

- Bugfix: Running `telepresence intercept --local-only --docker-run` no longer  results in a panic.
  Ticket [3171](https://github.com/telepresenceio/telepresence/issues/3171).

- Bugfix: Running `telepresence intercept --local-only --mount false` no longer results in an incorrect error message
  saying "a local-only intercept cannot have mounts".
  Ticket [3171](https://github.com/telepresenceio/telepresence/issues/3171).

- Bugfix: The helm chart now correctly handles custom `agentInjector.webhook.port` that was not being set in hook URLs.
  PR [3161](https://github.com/telepresenceio/telepresence/pull/3161).

- Bugfix: `.intercept.disableGlobal` and `.timeouts.agentArrival` are now correctly honored.

### 2.13.2 (May 12, 2023)
- Bugfix: Replaced `/` characters with a `-` when the authenticator service creates the kubeconfig in the Telepresence cache.
  PR [3167](https://github.com/telepresenceio/telepresence/pull/3167).

- Feature: Configurable strategy (`auto`, `powershell`. or `registry`) to set the global DNS search path on Windows. Default
  is `auto` which means try `powershell` first, and if it fails, fall back to `registry`.
  Ticket [3152](https://github.com/telepresenceio/telepresence/issues/3152).

- Feature: The timeout for the traffic manager to wait for traffic agent to arrive can
  now be configured in the `values.yaml` file using `timeouts.agentArrival`. The default
  timeout is still 30 seconds.
  PR [3148](https://github.com/telepresenceio/telepresence/pull/3148).

- Bugfix: The automatic discovery of a local container based cluster (minikube or kind) used when the
  Telepresence daemon runs in a container, now works on macOS and Windows, and with different profiles,
  ports, and cluster names
  PR [3165](https://github.com/telepresenceio/telepresence/pull/3165).

- Bugfix: FTP Stability improvements. Multiple simultaneous intercepts can transfer large files in bidirectionally and in parallel.
  PR [3157](https://github.com/telepresenceio/telepresence/pull/3157).

- Bugfix: Pods using persistent volumes no longer causes timeouts when intercepted.

- Bugfix: Ensure that `telepresence connect` succeeds even though DNS isn't configured correctly.
  Ticket [3143](https://github.com/telepresenceio/telepresence/issues/3143).
  PR [3154](https://github.com/telepresenceio/telepresence/pull/3154).

- Bugfix: The traffic-manager would sometimes panic with a "close of closed channel" message and exit.
  PR [3160](https://github.com/telepresenceio/telepresence/pull/3160).

- Bugfix: The traffic-manager would sometimes panic and exit after some time due to a type cast panic.
  Ticket [3149](https://github.com/telepresenceio/telepresence/issues/3149).

### 2.13.1 (April 20, 2023)

- Change: Update ambassador-agent to version 1.13.13

### 2.13.0 (April 18, 2023)

- Feature: The Docker network used by a Kind or Minikube (using the "docker" driver) installation, is automatically
  detected and connected to a Docker container running the Telepresence daemon.

- Feature: Mapped namespaces are included in the output of the `telepresence status` command.

- Feature: There's a new --address flag to the intercept command allowing users to set the target IP of the intercept.

- Feature: The new flags `--docker-build`, and `--docker-build-opt` was added to `telepresence intercept` to facilitate a
  docker run directly from a docker context.

- Bugfix: Using `telepresence intercept --docker-run` now works with a container based daemon started with `telepresence connect --docker`

- Bugfix: DNS works properly even when no cluster subnet is routed by the Telepresence VIF.

- Bugfix: The Traffic Manager uses a fail-proof way to determine the cluster domain.

- Bugfix: DNS on windows is more reliable and performant.

- Bugfix: The agent is now correctly injected even with a high number of deployment starting at the same time.

- Bugfix: The kubeconfig is made self-contained before running Telepresence daemon in a Docker container.

- Bugfix: The client will no longer need cluster wide permissions when connected to a namespace scoped Traffic Manager.

- BugFix: The version command won't throw an error anymore if there is no kubeconfig file defined.

### 2.12.2 (April 4, 2023)

- Security: Update golang to 1.20.3 to address CVE-2023-24534, CVE-2023-24536, CVE-2023-24537, CVE-2023-24538

### 2.12.1 (March 22, 2023)

- Bugfix: Illegal characters are now replaced when a docker container name is generated from a kubernetes context name.

### 2.12.0 (March 20, 2023)

- Feature: Telepresence can now start or connect to a daemon in a docker container by use of the global `--docker` flag.

- Feature: Adds an authenticator package to support integration with the [client-go credential](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#client-go-credential-plugins) plugins when the
  daemon runs in a docker container.

- Feature: The `telepresence helm` command now accepts a `--namespace` flag.

- Change: Telepresence will now detect if services and pods are routable, independently of one another, before adding their routes. This is a change from before, when being already able to connect to pods prevented the addition of routes for services too.

- Bugfix: The traffic-manager will no longer panic when the CNAME of kubernetes.default doesn't contain .svc.

- Bugfix: The `telepresence helm install/upgrade --set` family of flags now work correctly with comma separated values.

### 2.11.1 (February 27, 2023)

- Bugfix: The multi-arch build now for the proprietary traffic-manager and traffic-agent now works for both amd64 and arm64.

### 2.11.0 (February 22, 2023)

- Feature: When Telepresence detects that it runs in a docker container, it will now expose its DNS on `localhost:53`. This makes the
  container itself a DNS server. Very handy when other containers use `--network container:[tp-container]`.

- Feature: A new flag, `--local-mount-port <port>` will make `telepresence intercept --detailed-output --output=[yaml|json]` create
  a bridge to the remote SFTP service instead of starting an sshfs client. This enables the sshfs client to be started outside of the
  container, and thus, mount filesystems that then can be used as source for volumes that other containers will use.

- Feature: The Telepresence daemon can now run as a long-lived process in a docker container so that CLI commands that
  run in other containers can use a common daemon for network access and intercepts.

- Feature: A new boolean flag `--detailed-output` was added to the `telepresence intercept` command. It will output very
  detailed information about an intercept when used together with `--output=[json|yaml]`.
  Pull Request [3013](https://github.com/telepresenceio/telepresence/pull/3013).

- Feature: IPv6 support. Thanks to [@0x6a77](https://www.github.com/0x6a77).
  Ticket [2978](https://github.com/telepresenceio/telepresence/issues/2978).

- Feature: Adds two parameters `--also-proxy` and `--never-proxy` to the `telepresence connect` command.
  Ticket [2950](https://github.com/telepresenceio/telepresence/issues/2950).

- Feature: Add a parameter `--manager-namespace` to the `telepresence connect` command.
  Ticket [2968](https://github.com/telepresenceio/telepresence/issues/2968)

- Feature: Add a configuration `cluster.defaultManagerNamespace` for setting the default manager namespace.
  Ticket [2968](https://github.com/telepresenceio/telepresence/issues/2968)

- Change: The namespace of the connected manager is now displayed in the `telepresence status` output.
  Ticket [2968](https://github.com/telepresenceio/telepresence/issues/2968)

- Change: Depreciate `--watch` flag in `telepresence list` command. This is now covered by `--output json-stream`

- Change: Add `--output` option `json-stream`

- Bugfix: Fixed a bug when detecting VPN conflicts on macOS that removed conflicting gateway links.

- Bugfix: Fixed a bug where connecting to certain VPNs that map the CIDR range of the cluster would result in no routes getting added.
  Ticket [3006](https://github.com/telepresenceio/telepresence/issues/3006)

- Bugfix: Support ARM64 architecture
  Ticket [2786](https://github.com/telepresenceio/telepresence/issues/2786)

### 2.10.6 (February 14, 2023)

Security release to rebuild with go 1.19.6

### 2.10.5 (February 6, 2023)

- Change: mTLS Secrets will now be mounted into the traffic agent, instead of expected to be read by it from the API.

- Bugfix: Fixed a bug that prevented the local daemons from automatically reconnecting to the traffic manager when the network connection was lost.

### 2.10.4 (January 20, 2023)

- Bugfix: Fix backward compatibility issue when using traffic-managers of version 2.9.5 or older.

### 2.10.2 (January 16, 2023)

- Bugfix: Ensure that CLI and user-daemon binaries are the same version when running `telepresence helm install`
  or `telepresence helm upgrade`.

### 2.10.1 (January 11, 2023)

- Bugfix: Fixed a regex in our release process.

### 2.10.0 (January 11, 2023)

- Feature: The Traffic Manager can now be set to either "team" mode or "single user" mode.
  When in team mode, intercepts will default to http intercepts.

- Feature: The `telepresence helm` sub-commands `insert` and `upgrade` now accepts all types
  of helm `--set-XXX` flags.

- Feature: A new `telepresence helm upgrade` command was added with the additional flags
  `--reuse-values` and `--reset-values`. This means that the `telpresence helm install --upgrade`
  flag has been deprecated.

- Feature: Image pull secrets for the traffic-agent can now be added using the Helm chart setting
  `agent.image.pullSecrets`.

- Change: The configmap `traffic-manager-clients` has been renamed to `traffic-manager`.

- Change: The Helm installation will now fail if `intercept.disableGlobal=true` and `traffiManager.mode`
  is not set to `team`.

- Change: If the cluster is Kubernetes 1.21 or later, the mutating webhook will find the correct namespace
  using the label `kubernetes.io/metadata.name` rather than `app.kuberenetes.io/name`. Ticket [2913](https://github.com/telepresenceio/telepresence/issues/2913).

- Change: The name of the mutating webhook now contains the namespace of the traffic-manager so
  that the webhook is easier to identify when there are multiple namespace scoped telepresence
  installations in the cluster.

- Change: The OSS Helm chart is no longer pushed to the datawire Helm repository. It will
  instead be pushed from the telepresence proprietary repository. The OSS Helm chart is still
  what's embedded in the OSS telepresence client. PR [2943](https://github.com/telepresenceio/telepresence/pull/2943).

- Bugfix: Telepresence no longer panics when `--docker-run` is combined with `--name <name>` instead of
  `--name=<name>`. Ticket [2953](https://github.com/telepresenceio/telepresence/issues/2953).

- Bugfix: Telepresence traffic-manager extracts the cluster domain (e.g. "cluster.local") using a CNAME lookup for "kubernetes.default"
  instead of "kubernetes.default.svc".

- Bugfix: A timeout was added to the pre-delete hook `uninstall-agents`, so that a helm uninstall doesn't
  hang when there is no running traffic-manager. PR [2937](https://github.com/telepresenceio/telepresence/pull/2937).

### 2.9.5 (December 8, 2022)

- Security: Update golang to 1.19.4 to address
  [CVE-2022-41720 and CVE-2022-41717](https://groups.google.com/g/golang-announce/c/L_3rmdT0BMU).

- Bugfix: A regression that was introduced in 2.9.3, preventing use of gce authentication without also
  having a config element present in the gce configuration in the kubeconfig, has been fixed.

### 2.9.4 (December 5, 2022)

- Feature: The traffic-manager can automatically detect that the node subnets are different from the
  pod subnets, and switch detection strategy to instead use subnets that cover the pod IPs.

- Bugfix: The `telepresence helm` command `--set x=y` flag didn't correctly set values of other types
  than `string`. The code now uses standard Helm semantics for this flag.

- Bugfix: Telepresence now uses the correct `agent.image` properties in the Helm chart when copying
  agent image settings from the `config.yml` file.

- Bugfix: Initialization of FTP type file sharing is delayed, so that setting it using the Helm chart
  value `intercept.useFtp=true` works as expected.

- Bugfix: The port-forward that is created when Telepresence connects to a cluster is now properly
  closed when `telepresence quit` is called.

- Bugfix: The user daemon no longer panics when the `config.yml` is modified at a time when the user daemon
  is running but no session is active.

- Bugfix: Fix race condition that would occur when `telepresence connect` `telepresence leave` was called
  several times in rapid succession.

### 2.9.3 (November 23, 2022)

- Feature: The helm chart now supports `livenessProbe` and `readinessProbe` for the traffic-manager
  deployment, so that the pod automatically restarts if it doesn't respond.

- Change: The root daemon now communicates directly with the traffic-manager instead of routing all
  outbound traffic through the user daemon.

- Change: The output of `telepresence version` is now aligned and no longer contains "(api v3)"

- Bugfix: Using `telepresence loglevel LEVEL` now also sets the log level in the root daemon.

- Bugfix: Multi valued kubernetes flags such as `--as-group` are now propagated correctly.

- Bugfix: The root daemon would sometimes hang indefinitely when quit and connect were called
  in rapid succession.

- Bugfix: Don't use `systemd.resolved` base DNS resolver unless cluster is proxied.

### 2.9.2 (November 16, 2022)

- Bugfix: Fix panic when connecting to an older traffic-manager.

- Bugfix: Fix `http-header` flag sometimes wouldn't propagate correctly.

### 2.9.1 (November 15, 2022)

- Bugfix: Fix regression in 2.9.0 causing `no Auth Provider found for name “gcp”` when connecting.

### 2.9.0 (November 15, 2022)

- Feature: A new `telepresence config view` command was added that shows how the client is currently configured.

- Feature: The traffic-manager can now configure all clients that connect through the `client:` map in
  the `values.yaml` file.

- Feature: The traffic-manager version is now included in the output from the `telepresence version` command.
- Feature: add `podLabels` values to Helm Chart to add extra labels to deployment.

- Feature: The telepresence flag `--output` now accepts `yaml` as a valid format.

- Change: The `telepresence status --json` flag is deprecated. Use `telepresence status --output=json` instead.

- Bugfix: Informational messages that don't really originate from the command, such as "Launching Telepresence Root Daemon",
  or "An update of telepresence ...", are discarded instead of being printed as plain text before the actual formatted
  output when using the `--output=json`.

- Bugfix: An attempt to use an invalid value for the global `--output` flag now renders a proper error message.

- Bugfix: Unqualified service names now resolves OK when using `telepresence intercept --docker-run`.

- Bugfix: Files lingering under /etc/resolver on macOS are now removed when a new root daemon starts.

### 2.8.5 (November 2, 2022)

- Change: This is a security release. It's identical with 2.8.3 but built using Go 1.19.3 to address
  [CVE-2022-41716 and Go issue https://go.dev/issue/56284](https://github.com/golang/go/issues/56284).

### 2.8.4 (November 2, 2022)

- Change: Failed security release. Use 2.8.5.

### 2.8.3 (October 27, 2022)

- Feature: The traffic-manager can be configured to disable global (non-http) intercepts using the
  Helm chart setting `intercept.disableGlobal`.

- Feature: The port used for the mutating webhook can be configured using the Helm chart setting
  `agentInjector.webhook.port`.

- Feature: A new repeated `--set a.b.c=v` flag was added to the `telepresence helm install` command so that
  values can be passed directly from the command line, without first storing them in a file.

- Change: The default port for the mutating webhook is now `443`. It used to be `8443`.

- Change: The traffic-manager will no longer default to use the `tel2` image for the traffic-agent when it is
  unable to connect to Ambassador Cloud. Air-gapped environments must declare what image to use in the Helm chart.

- Bugfix: `telepresence connect` now works as long as the traffic manager is installed, even if
  it wasn't installed via `helm install`

- Bugfix: Telepresence check-vpn no longer crashes when the daemons don't start properly.

- Bugfix: The root daemon no longer crashes when the session boot times out before the cluster connection succeeds.

### 2.8.2 (October 15, 2022)

- Feature: The Telepresence DNS resolver is now capable of resolving queries of type `A`, `AAAA`, `CNAME`,
  `MX`, `NS`, `PTR`, `SRV`, and `TXT`.

- Feature: A new `client` struct was added to the Helm chart. It contains a `connectionTTL` that controls
  how long the traffic manager will retain a client connection without seeing any sign of life from the client.

- Feature: A `dns` struct container the fields `includeSuffixes` and `excludeSuffixes` was added to the Helm
  chart `client` struct, making those values configurable per cluster.

- Feature: The API port used by the traffic-manager is now configurable using the Helm chart value `apiPort`.
  The default port is 8081.

- Change: The Helm chart `dnsConfig` was deprecated but retained for backward compatibility. The fields
  `alsoProxySubnets` and `neverProxySubnets` can now be found under `routing` in the `client` struct.

- Change: The Helm chart `agentInjector.agentImage` was moved to `agent.image`. The old value is deprecated but
  retained for backward compatibility.

- Change: The Helm chart `agentInjector.appProtocolStrategy` was moved to `agent.appProtocolStrategy`. The old
  value is deprecated but retained for backward compatibility.

- Change: The Helm chart `dnsServiceName`, `dnsServiceNamespace`, and `dnsServiceIP` has been removed, because
  they are no longer needed. The TUN-device will use the traffic-manager pod-IP on platforms where it needs to
  dedicate an IP for its local resolver.

- Bugfix: Environment variable interpolation now works for all definitions that are copied from pod containers
  into the injected traffic-agent container.

- Bugfix: An attempt to create simultaneous intercepts that span multiple namespace on the same workstation
  is detected early and prohibited instead of resulting in failing DNS lookups later on.

- Bugfix: Spurious and incorrect ""!! SRV xxx"" messages will no longer appear in the logs when the reason
  is normal context cancellation.

- Bugfix: Single label names now resolves correctly when using Telepresence in Docker on a Linux host

- Bugfix: The Helm chart value `appProtocolStrategy` is now correctly named (used to be `appPortStategy`)
- Bugfix: Include file name in error message when failing to parse JSON file.

### 2.7.6 (September 16, 2022)

- Reintroduce everything from 2.7.4 with fix for issue preventing the CLI from launching on arm64 builds

### 2.7.5 (September 14, 2022)

- Revert of release 2.7.5 (so essentially the same as 2.7.3)

### 2.7.4 (September 14, 2022)

- Feature: The `resources` for the traffic-agent container and the optional init container can
  be specified in the Helm chart using the `resource` and `initResource` fields of the
  `agentInjector.agentImage`.

- Feature: When the traffic-manager fails to inject a traffic-agent, the cause for the failure is
  detected by reading the cluster events, and propagated to the user.

- Feature: Telepresence can now use an embedded FTP client and load an existing FUSE library
  instead of running an external `sshfs` or `sshfs-win` binary. This feature is experimental
  in 2.7.x and enabled by setting `intercept.useFtp` to `true` in the `config.yml`.

- Change: Telepresence on Windows upgraded winfsp from version 1.10 to 1.11

- Bugfix: Running CLI commands on Apple M1 machines will no longer throw warnings about `/proc/cpuinfo`
  and `/proc/self/auxv`.

### 2.7.3 (September 7, 2022)

- Bugfix: CLI commands that are executed by the user daemon now use a pseudo TTY. This enables
  `docker run -it` to allocate a TTY and will also give other commands like `bash read` the
  same behavior as when executed directly in a terminal.

- Bugfix: The traffic-manager will no longer log numerous warnings saying: "Issuing a
  systema request without ApiKey or InstallID may result in an error".

- Bugfix: The traffic-manager will no longer log an error saying: "Unable to derive subnets
  from nodes" when the `podCIDRStrategy` is `auto` and it chooses to instead derive the
  subnets from the pod IPs.

### 2.7.2 (August 25, 2022)

- Bugfix: Standard I/O is restored when using `telepresence intercept <opts> -- <command>`.

- Bugfix: Graciously handle nil intercept environment from the traffic-manager.

- Feature: The timeout for the initial connectivity check that Telepresence performs
  in order to determine if the cluster's subnets are proxied or not can now be configured
  in the `config.yml` file using `timeouts.connectivityCheck`. The default timeout was
  changed from 5 seconds to 500 milliseconds to speed up the actual connect.

- Feature: Adds cli autocompletion for the `--namespace` flag on the `list` and `intercept` commands,
  autocompletion for interceptable workloads on the `intercept` command, and autocompletion for
  active intercepts on the `leave` command.

- Change: The command `telepresence gather-traces` now prints out a message on success.
- Change: The command `telepresence upload-traces` now prints out a message on success.
- Change: The command `telepresence gather-traces` now traces itself and reports errors with trace gathering

- Change: The `cli.log` log is now logged at the same level as the `connector.log`

- Bugfix: Streams created between the traffic-agent and the workstation are now properly closed
  when no interceptor process has been started on the workstation. This fixes a potential problem where
  a large number of attempts to connect to a non-existing interceptor would cause stream congestion
  and an unresponsive intercept.

- Bugfix: Telepresence help message functionality without a running user daemon has been restored.

- Bugfix: The `telepresence list` command no longer includes the `traffic-manager` deployment.

### 2.7.1 (August 10, 2022)

- Change: The command `telepresence uninstall` has been restored, but the `--everything` flag is now deprecated.

- Change: `telepresence helm uninstall` will only uninstall the traffic-manager and no longer accepts the `--everything`, `--agent`,
  or `--all-agents` flags.

- Bugfix: `telepresence intercept` will attempt to connect to the traffic manager before creating an intercept.

### 2.7.0 (August 8, 2022)

- Feature: `telepresence intercept` has gained a
  `--preview-url-add-request-headers` flag (and `telepresence preview create` a `--add-request-headers` flag) that can be used to inject
  request headers in to every request made through the preview URL.

- Feature: The Docker image now contains a new program in addition to
  the existing traffic-manager and traffic-agent: the pod-daemon. The
  pod-daemon is a trimmed-down version of the user-daemon that is
  designed to run as a sidecar in a Pod, enabling CI systems to create
  preview deploys.

- Feature: The Telepresence components now collect OpenTelemetry traces.
  Up to 10MB of trace data are available at any given time for collection from
  components. `telepresence gather-traces` is a new command that will collect
  all that data and place it into a gzip file, and `telepresence upload-traces` is
  a new command that will push the gzipped data into an OTLP collector.

- Feature: The agent injector now supports a new annotation, `telepresence.getambassador.io/inject-ignore-volume-mounts`, that can be used to make the injector ignore specified volume mounts denoted by a comma-separated string.

- Change: The traffic manager is no longer automatically installed into the cluster. Connecting or creating an intercept in a cluster without a traffic manager will return an error.

- Feature: A new telepresence helm command was added to provide an easy way to install, upgrade, or uninstall the telepresence traffic-manager.

- Change: The command `telepresence uninstall` has been moved to `telepresence helm uninstall`.

- Change: Add an emptyDir volume and volume mount under `/tmp` on the agent sidecar so it works with `readOnlyRootFileSystem: true`

- Feature: Added prometheus support to the traffic manager.

### 2.6.8 (June 23, 2022)

- Feature: The name and namespace for the DNS Service that the traffic-manager uses in DNS auto-detection can now be specified.

- Feature: Should the DNS auto-detection logic in the traffic-manager fail, users can now specify a fallback IP to use.

- Feature: It is now possible to intercept UDP ports with Telepresence and also use `--to-pod` to forward UDP
  traffic from ports on localhost.

- Change: The Helm chart will now add the `nodeSelector`, `affinity` and `tolerations` values to the traffic-manager's
  post-upgrade-hook and pre-delete-hook jobs.

- Bugfix: Telepresence no longer fails to inject the traffic agent into the pod generated for workloads that have no
  volumes and `automountServiceAccountToken: false`.

- Feature: The helm-chart now supports settings resources, securityContext and podSecurityContext for use with chart hooks.

### 2.6.7 (June 22, 2022)

- Bugfix: The Telepresence client will remember and reuse the traffic-manager session after a network failure
  or other reason that caused an unclean disconnect.

- Bugfix: Telepresence will no longer forward DNS requests for "wpad" to the cluster.

- Bugfix: The traffic-agent will properly shut down if one of its goroutines errors.

### 2.6.6 (June 9, 2022)

- Bugfix: The propagation of the `TELEPRESENCE_API_PORT` environment variable now works correctly.

- Bugfix: The `--output json` global flag no longer outputs multiple objects.

### 2.6.5 (June 3, 2022)

- Feature: The `reinvocationPolicy` or the traffic-agent injector webhook can now be configured using the Helm chart.

- Feature: The traffic manager now accepts a root CA for a proxy, allowing it to connect to ambassador cloud from behind an HTTPS proxy.
  This can be configured through the helm chart.

- Feature: A policy that controls when the mutating webhook injects the traffic-agent was added, and can be configured in the Helm chart.

- Change: Telepresence on Windows upgraded wintun.dll from version 0.12 to version 0.14.1

- Change: Telepresence on Windows upgraded winfsp from version 1.9 to 1.10

- Change: Telepresence upgraded its embedded Helm from version 3.8.1 to 3.9

- Change: Telepresence upgraded its embedded Kubernetes API from version 0.23.4 to 0.24.1

- Feature: Added a `--watch` flag to `telepresence list` that can be used to watch interceptable workloads.

- Change: The configuration setting for `images.webhookAgentImage` is now deprecated. Use `images.agentImage` instead.

- Bugfix: The `reinvocationPolicy` or the traffic-agent injector webhook now defaults to `Never` insteadof `IfNeeded` so
  that `LimitRange`s on namespaces can inject a missing `resources` element into the injected traffic-agent container.

- Bugfix: UDP based communication with services in the cluster now works as expected.

- Bugfix: The command help will only show Kubernetes flags on the commands that supports them

- Bugfix: Only the errors from the last session will be considered when counting the number of errors in the log after
  a command failure.

### 2.6.4 (May 23, 2022)

- Bugfix: The traffic-manager RBAC grants permissions to update services, deployments, replicatsets, and statefulsets. Those
  permissions are needed when the traffic-manager upgrades from versions < 2.6.0 and can be revoked after the upgrade.

### 2.6.3 (May 20, 2022)

- Bugfix: The `--mount` intercept flag now handles relative mount points correctly on non-windows platforms. Windows
  still require the argument to be a drive letter followed by a colon.

- Bugfix: The traffic-agent's configuration update automatically when services are added, updated or deleted.

- Bugfix: The `--mount` intercept flag now handles relative mount points correctly on non-windows platforms. Windows
  still require the argument to be a drive letter followed by a colon.

- Bugfix: The traffic-agent's configuration update automatically when services are added, updated or deleted.

- Bugfix: Telepresence will now always inject an initContainer when the service's targetPort is numeric

- Bugfix: Workloads that have several matching services pointing to the same target port are now handled correctly.

- Bugfix: A potential race condition causing a panic when closing a DNS connection is now handled correctly.

- Bugfix: A container start would sometimes fail because and old directory remained in a mounted temp volume.

### 2.6.2 (May 17, 2022)

- Bugfix: Workloads controlled by workloads like Argo `Rollout` are injected correctly.

- Bugfix: Multiple services appointing the same container port no longer result in duplicated ports in an injected pod.

- Bugfix: The `telepresence list` command no longer errors out with "grpc: received message larger than max" when listing namespaces
  with a large number of workloads.

### 2.6.1 (May 16, 2022)

- Bugfix: Telepresence will now handle multiple path entries in the KUBECONFIG environment correctly.

- Bugfix: Telepresence will no longer panic when using preview URLs with traffic-managers < 2.6.0

- Change: Traffic-manager now attempts to obtain a cluster id from the license if it could not obtain it from the Kubernetes API.

### 2.6.0 (May 13, 2022)

- Feature: Traffic-agent is now capable of intercepting multiple containers and multiple ports per container.

- Feature: Telepresence client now require less RBAC permissions in order to intercept.

- Change: All pod-injection is performed by the mutating webhook. Client will no longer modify workloads.

- Change: Traffic-agent is configured using a ConfigMap entry. In prior versions, the configuration was passed in the container environment.

- Change: The helm-chart no longer has a default set for the agentInjector.image.name, and unless its set, the traffic-manager will ask
  SystemA for the preferred image.

- Change: Client no longer needs RBAC permissions to update deployments, replicasets, and statefulsets.

- Change: Telepresence now uses Helm version 3.8.1 when installing the traffic-manager

- Change: The traffic-manager will not accept connections from clients older than 2.6.0. It can't, because they still use the old way of
  injecting the agent by modifying the workload.

- Change: When upgrading, all workloads with injected agents will have their agent "uninstalled" automatically. The mutating webhook will
  then ensure that their pods will receive an updated traffic-agent.

- Bugfix: Remote mounts will now function correctly with custom `securityContext`.

- Bugfix: The help for commands that accept kubernetes flags will now display those flags in a separate group.

- Bugfix: Using `telepresence leave` or `telepresence quit` on an intercept that spawned a command using `--` on the command line
  will now terminate that command since it's considered parented by the intercept that is removed.

- Change: Add support for structured output as JSON by setting the global --output=json flag.

### 2.5.8 (April 27, 2022)

- Bugfix: Telepresence now ensures that the download folder for the enhanced free client is created prior to downloading it.

### 2.5.7 (April 25, 2022)

- Change: A namespaced traffic-manager will no longer require cluster wide RBAC. Only Roles and RoleBindings are now used.

- Bugfix: The DNS recursion detector didn't work correctly on Windows, resulting in sporadic failures to resolve names
  that were resolved correctly at other times.

- Bugfix: A telepresence session will now last for 24 hours after the user's last connectivity. If a session expires, the connector will automatically try to reconnect.

### 2.5.6 (April 15, 2022)

- Bugfix: The `gather-logs` command will no longer send any logs through `gRPC`.

- Change: Telepresence agents watcher will now only watch namespaces that the user has accessed since the last `connect`.

### 2.5.5 (April 8, 2022)

- Change: The traffic-manager now requires permissions to read pods across namespaces even if installed with limited permissions

- Bugfix: The DNS resolver used on Linux with systemd-resolved now flushes the cache when the search path changes.

- Bugfix: The `telepresence list` command will produce a correct listing even when not preceded by a `telepresence connect`.

- Bugfix: The root daemon will no longer get into a bad state when a disconnect is rapidly followed by a new connect.

- Bugfix: The client will now only watch agents from accessible namespaces, and is also constrained to namespaces explicitly mapped
  using the `connect` command's `--mapped-namespaces` flag.

- Bugfix: The `gather-logs` command will only gather traffic-agent logs from accessible namespaces, and is also constrained to namespaces
  explicitly mapped using the `connect` command's `--mapped-namespaces` flag.

### 2.5.4 (March 29, 2022)

- Change: The list command, when used with the `--intercepts` flag, will list the users intercepts from all namespaces

- Change: The status command includes the install id, user id, account id, and user email in its result, and can print output as JSON

- Change: The lookup-timeout config flag used to set timeouts for DNS queries resolved by a cluster now also configures the timeout for fallback queries (i.e. queries not resolved by the cluster) when connected to the cluster.

- Change: The TUN device will no longer route pod or service subnets if it is running in a machine that's already connected to the cluster

- Bugfix: The client's gather logs command and agent watcher will now respect the configured grpc.maxReceiveSize

- Bugfix: Client and agent sessions no longer leaves dangling waiters in the traffic-manager when they depart.

- Bugfix: An advice to "see logs for details" is no longer printed when the argument count is incorrect in a CLI command.

- Bugfix: Removed a bad concatenation that corrupted the output path of `telepresence gather-logs`.

- Bugfix: Agent container is no longer sensitive to a random UID or an UID imposed by a SecurityContext.

- Bugfix: Intercepts that fail to create are now consistently removed to prevent non-working dangling intercepts from sticking around.

- Bugfix: The ingress-l5 flag will no longer be forcefully set to equal the --ingress-host flag

- Bugfix: The DNS fallback resolver on Linux now correctly handles concurrent requests without timing them out

### 2.5.3 (February 25, 2022)

- Feature: Client-side binaries for the arm64 architecture are now available for linux

- Bugfix: Fixed bug in the TCP stack causing timeouts after repeated connects to the same address

### 2.5.2 (February 23, 2022)

- Bugfix: Fixed a bug where Telepresence would use the last server in resolv.conf

### 2.5.1 (February 19, 2022)

- Bugfix: Fixed a bug where using a GKE cluster would error with: No Auth Provider found for name "gcp"

### 2.5.0 (February 18, 2022)

- Feature: The flags `--http-path-equal`, `--http-path-prefix`, and `--http-path-regex` can can be used in addition to the `--http-match`
  flag to filter personal intercepts by the request URL path

- Feature: The flag `--http-meta` can be used to declare metadata key value pairs that will be returned by the Telepresence rest API
  endpoint /intercept-info

- Feature: Telepresence Login now prompts you to optionally install an enhanced free client, which has some additional features when used with Ambassador Cloud.

- Change: Logs generated by the CLI are no longer discarded. Instead, they will end up in `cli.log`.

- Change: Both daemon logfiles now rotate daily instead of once for each new connect

- Change: The flag `--http-match` was renamed to `--http-header`. Old flag still works, but is deprecated and doesn't
  show up in the help.

- Change: The verb "watch" was added to the set of required verbs when accessing services and workloads for the client RBAC ClusterRole

- Change: Telepresence is no longer backward compatible with versions 2.4.4 or older because the deprecated multiplexing tunnel functionality was removed.

- Change: The global networking flags are no longer global. Using them will render a deprecation warning unless they are supported by the command.
  The subcommands that support networking flags are `connect`, `current-cluster-id`, and `genyaml`.

- Change: Telepresence now includes GOARCH of the binary in the metadata reported.

- Bugfix: The also-proxy and never-proxy subnets are now displayed correctly when using the `telepresence status` command

- Bugfix: Telepresence will no longer require `SETENV` privileges when starting the root daemon.

- Bugfix: Telepresence will now parse device names containing dashes correctly when determining routes that it should never block.

- Bugfix: The cluster domain (typically "cluster.local") is no longer added to the DNS `search` on Linux using `systemd-resolved`. Instead,
  it is added as a `domain` so that names ending with it are routed to the DNS server.

- Bugfix: Fixed a bug where the `--json` flag did not output json for `telepresence list` when there were no workloads.

- Change: Updated README file with more details about the project.

- Bugfix: Fixed a bug where the overriding DNS resolver would break down in Linux if /etc/resolv.conf listed an ipv6 resolver

### 2.4.11 (February 10, 2022)

- Change: Include goarch metadata for reporting to distinguish between Intel and Apple Silicon Macs

### 2.4.10 (January 13, 2022)

- Feature: The flag `--http-plaintext` can be used to ensure that an intercept uses plaintext http or grpc when
  communicating with the workstation process.

- Feature: The port used by default in the `telepresence intercept` command (8080), can now be changed by setting
  the `intercept.defaultPort` in the `config.yml` file.

- Feature: The strategy when selecting the application protocol for personal intercepts in agents injected by the
  mutating webhook can now be configured using the `agentInjector.appProtocolStrategy` in the Helm chart.

- Feature: The strategy when selecting the application protocol for personal intercepts can now be configured using
  the `intercept.appProtocolStrategy` in the `config.yml` file.

- Change: Telepresence CI now runs in GitHub Actions instead of Circle CI.

- Bugfix: Telepresence will no longer log invalid: "unhandled connection control message: code DIAL_OK" errors.

- Bugfix: User will not be asked to log in or add ingress information when creating an intercept until a check has been
  made that the intercept is possible.

- Bugfix: Output to `stderr` from the traffic-agent's `sftp` and the client's `sshfs` processes are properly logged as errors.

- Bugfix: Auto installer will no longer not emit backslash separators for the `/tel-app-mounts` paths in the
  traffic-agent container spec when running on Windows

### 2.4.9 (December 9, 2021)

- Bugfix: Fixed an error where access tokens were not refreshed if you log in
  while the daemons are already running.

- Bugfix: A helm upgrade using the --reuse-values flag no longer fails on a "nil pointer" error caused by a nil `telpresenceAPI` value.

### 2.4.8 (December 3, 2021)

- Feature: A RESTful service was added to Telepresence, both locally to the client and to the `traffic-agent` to help determine if messages with a set of headers should be
  consumed or not from a message queue where the intercept headers are added to the messages.

- Change: The environment variable TELEPRESENCE_LOGIN_CLIENT_ID is no longer used.

- Feature: There is a new subcommand, `test-vpn`, that can be used to diagnose connectivity issues with a VPN.

- Bugfix: The tunneled network connections between Telepresence and
  Ambassador Cloud now behave more like ordinary TCP connections,
  especially around timeouts.

### 2.4.7 (November 24, 2021)

- Feature: The agent injector now supports a new annotation, `telepresence.getambassador.io/inject-service-name`, that can be used to set the name of the service to be intercepted.
  This will help disambiguate which service to intercept for when a workload is exposed by multiple services, such as can happen with Argo Rollouts

- Feature: The kubeconfig extensions now support a `never-proxy` argument, analogous to `also-proxy`, that defines a set of subnets that will never be proxied via telepresence.

- Feature: Added flags to "telepresence intercept" that set the ingress fields as an alternative to using the dialogue.

- Change: Telepresence check the versions of the client and the daemons and ask the user to quit and restart if they don't match.

- Change: Telepresence DNS now uses a very short TTL instead of explicitly flushing DNS by killing the `mDNSResponder` or doing `resolvectl flush-caches`

- Bugfix: Legacy flags such as `--swap-deployment` can now be used together with global flags.

- Bugfix: Outbound connections are now properly closed when the peer closes.

- Bugfix: The DNS-resolver will trap recursive resolution attempts (may happen when the cluster runs in a docker-container on the client).

- Bugfix: The TUN-device will trap failed connection attempts that results in recursive calls back into the TUN-device (may happen when the
  cluster runs in a docker-container on the client).

- Bugfix: Fixed a potential deadlock when a new agent joined the traffic manager.

- Bugfix: The app-version value of the Helm chart embedded in the telepresence binary is now automatically updated at build time. The value is hardcoded in the
  original Helm chart when we release so this fix will only affect our nightly builds.

- Bugfix: The configured webhookRegistry is now propagated to the webhook installer even if no webhookAgentImage has been set.

- Bugfix: Login logs the user in when their access token has expired, instead of having no effect.

### 2.4.6 (November 2, 2021)

- Feature: Telepresence CLI is now built and published for Apple Silicon Macs.

- Feature: Telepresence now supports manually injecting the traffic-agent YAML into workload manifests.
  Use the `genyaml` command to create the sidecar YAML, then add the `telepresence.getambassador.io/manually-injected: "true"` annotation to your pods to allow Telepresence to intercept them.

- Feature: Added a json flag for the "telepresence list" command. This will aid automation.

- Change: `--help` text now includes a link to https://www.telepresence.io/ so users who download Telepresence via Brew or some other mechanism are able to find the documentation easily.

- Bugfix: Telepresence will no longer attempt to proxy requests to the API server when it happens to have an IP address within the CIDR range of pods/services.

### 2.4.5 (October 15, 2021)

- Feature: Intercepting headless services is now supported. It's now possible to request a headless service on whatever port it exposes and get a response from the intercept.

- Feature: Preview url questions have more context and provide "best guess" defaults.

- Feature: The `gather-logs` command added two new flags. One to anonymize pod names + namespaces and the other for getting the pod yaml of the `traffic-manager` and any pod that contains a `traffic-agent`.

- Change: Use one tunnel per connection instead of multiplexing into one tunnel. This client will still be backwards compatible with older `traffic-manager`s that only support multiplexing.

- Bugfix: Telepresence will now log that the kubernetes server version is unsupported when using a version older than 1.17.

- Bugfix: Telepresence only adds the security context when necessary: intercepting a headless service or using a numeric port with the webhook agent injector.

### 2.4.4 (September 27, 2021)

- Feature: The strategy used by traffic-manager's discovery of pod CIDRs can now be configured using the Helm chart.

- Feature: Add the command `telepresence gather-logs`, which bundles the logs for all components
  into one zip file that can then be shared in a GitHub issue, in slack, etc. Use
  `telepresence gather-logs --help` to see additional options for running the command.

- Feature: The agent injector now supports injecting Traffic Agents into pods that have unnamed ports.

- Bugfix: The traffic-manager now uses less CPU-cycles when computing the pod CIDRs.

- Bugfix: If a deployment annotated with webhook annotations is deployed before telepresence is installed, telepresence will now install an agent in that deployment before intercept

- Bugfix: Fix an issue where the traffic-manager would sometimes go into a CPU loop.

- Bugfix: The TUN-device no longer builds an unlimited internal buffer before sending it when receiving lots of TCP-packets without PSH.
  Instead, the buffer is flushed when it reaches a size of 64K.

- Bugfix: The user daemon would sometimes hang when it encountered a problem connecting to the cluster or the root daemon.

- Bugfix: Telepresence correctly reports an intercept port conflict instead of panicking with segfault.

### 2.4.3 (September 15, 2021)

- Feature: The environment variable `TELEPRESENCE_INTERCEPT_ID` is now available in the interceptor's environment.

- Bugfix: A timing related bug was fixed that sometimes caused a "daemon did not start" failure.

- Bugfix: On Windows, crash stack traces and other errors were not
  written to the log files, now they are.

- Bugfix: On Linux kernel 4.11 and above, the log file rotation now
  properly reads the birth-time of the log file. On older kernels, it
  continues to use the old behavior of using the change-time in place
  of the birth-time.

- Bugfix: Telepresence will no longer refer the user to the daemon logs for errors that aren't related to
  problems that are logged there.

- Bugfix: The overriding DNS resolver will no longer apply search paths when resolving "localhost".

- Bugfix: The cluster domain used by the DNS resolver is retrieved from the traffic-manager instead of being
  hard-coded to "cluster.local".

- Bugfix: "Telepresence uninstall --everything" now also uninstalls agents installed via mutating webhook

- Bugfix: Downloading large files during an intercept will no longer cause timeouts and hanging traffic-agent.

- Bugfix: Passing false to the intercept command's --mount flag will no longer result in a filesystem being mounted.

- Bugfix: The traffic manager will establish outbound connections in parallel instead of sequentially.

- Bugfix: The `telepresence status` command reports correct DNS settings instead of "Local IP: nil, Remote IP: nil"

### 2.4.2 (September 1, 2021)

- Feature: A new `telepresence loglevel <level>` subcommand was added that enables changing the loglevel
  temporarily for the local daemons, the `traffic-manager` and the `traffic-agents`.

- Change: The default log-level is now `info` for all components of Telepresence.

- Bugfix: The overriding DNS resolver will no longer apply search paths when resolving "localhost".

- Bugfix: The RBAC was not updated in the helm chart to enable the traffic-manager to `get` and `list`
  namespaces, which would impact users who use licensed features of the Telepresence extensions in an
  air-gapped environment.

- Bugfix: The timeout for Helm actions wasn't always respected which could cause a failing install of the
  `traffic-manager` to make the user daemon to hang indefinitely.

### 2.4.1 (August 30, 2021)

- Bugfix: Telepresence will now mount all directories from `/var/run/secrets`, not just the kubernetes.io ones.
  This allows the mounting of secrets directories such as eks.amazonaws.com (for IRSA tokens)

- Bugfix: The grpc.maxReceiveSize setting is now correctly propagated to all grpc servers.
  This allows users to mitigate a root daemon crash when sending a message over the default maximum size.

- Bugfix: Some slight fixes to the `homebrew-package.sh` script which will enable us to run
  it manually if we ever need to make homebrew point at an older version.

- Feature: Helm chart has now a feature to on demand regenerate certificate used for mutating webhook by setting value.
  `agentInjector.certificate.regenerate`

- Change: The traffic-manager now requires `get` namespace permissions to get the cluster ID instead of that value being
  passed in as an environment variable to the traffic-manager's deployment.

- Change: The traffic-manager is now installed via an embedded version of the Helm chart when `telepresence connect` is first performed on a cluster.
  This change is transparent to the user.
  A new configuration flag, `timeouts.helm` sets the timeouts for all helm operations performed by the Telepresence binary.

- Bugfix: Telepresence will initialize the default namespace from the kubeconfig on each call instead of just doing it when connecting.

- Bugfix: The timeout to keep idle outbound TCP connections alive was increased from 60 to 7200 seconds which is the same as
  the Linux `tcp_keepalive_time` default.

- Bugfix: Telepresence will now remove a socket that is the result of an ungraceful termination and retry instead of printing
  an error saying "this usually means that the process has terminated ungracefully"

- Change: Failure to report metrics is logged using loglevel info rather than error.

- Bugfix: A potential deadlock situation is fixed that sometimes caused the user daemon to hang when the user
  was logged in.

- Feature: The scout reports will now include additional metadata coming from environment variables starting with
  `TELEPRESENCE_REPORT_`.

- Bugfix: The config setting `images.agentImage` is no longer required to contain the repository. The repository is
  instead picked from `images.repository`.

- Change: The `registry`, `webhookRegistry`, `agentImage` and `webhookAgentImage` settings in the `images` group of the `config.yml`
  now get their defaults from `TELEPRESENCE_AGENT_IMAGE` and `TELEPRESENCE_REGISTRY`.

### 2.4.0 (August 4, 2021)

- Feature: There is now a native Windows client for Telepresence.
  All the same features supported by the macOS and Linux client are available on Windows.

- Feature: Telepresence can now receive messages from the cloud and raise
  them to the user when they perform certain commands.

- Bugfix: Initialization of `systemd-resolved` based DNS sets
  routing domain to improve stability in non-standard configurations.

- Bugfix: Edge case error when targeting a container by port number.
  Before if your matching/target container was at containers list index 0,
  but if there was a container at index 1 with no ports, then the
  "no ports" container would end up the selected one

- Bugfix: A `$(NAME)` reference in the agent's environment will now be
  interpolated correctly.

- Bugfix: Telepresence will no longer print an INFO level log message when
  no config.yml file is found.

- Bugfix: A panic is no longer raised when passing an argument to the
  `telepresence intercept` option `--http-match` that doesn't contain an
  equal sign.

- Bugfix: The `traffic-manager` will only send subnet updates to a
  client root daemon when the subnets actually change.

- Bugfix: The agent uninstaller now distinguishes between recoverable
  and unrecoverable failures, allowing uninstallation from manually changed
  resources

### 2.3.7 (July 23, 2021)

- Feature: An `also-proxy` entry in the Kubernetes cluster config will
  show up in the output of the `telepresence status` command.

- Feature: `telepresence login` now has an `--apikey=KEY` flag that
  allows for non-interactive logins. This is useful for headless
  environments where launching a web-browser is impossible, such as
  cloud shells, Docker containers, or CI.

- Bugfix: Dialer will now close if it gets a ConnectReject. This was
  encountered when doing an intercept without a local process running
  and would result in requests hanging indefinitely.

- Bugfix: Made `telepresence list` command faster.

- Bugfix: Mutating webhook injector correctly hides named ports for probes.

- Bugfix: Initialization of `systemd-resolved` based DNS is more stable and
  failures causing telepresence to default to the overriding resolver will no
  longer cause general DNS lookup failures.

- Bugfix: Fixed a regression introduced in 2.3.5 that caused `telepresence current-cluster-id`
  to crash.

- Bugfix: New API keys generated internally for communication with
  Ambassador Cloud no longer show up as "no description" in the
  Ambassador Cloud web UI. Existing API keys generated by older
  versions of Telepresence will still show up this way.

- Bugfix: Fixed a race condition that logging in and logging out
  rapidly could cause memory corruption or corruption of the
  `user-info.json` cache file used when authenticating with Ambassador
  Cloud.

### 2.3.6 (July 20, 2021)

- Bugfix: Fixed a regression introduced in 2.3.5 that caused preview
  URLs to not work.

- Bugfix: Fixed a regression introduced in 2.3.5 where the Traffic
  Manager's `RoleBinding` did not correctly appoint the
  `traffic-manager` `Role`, causing subnet discovery to not be able to
  work correctly.

- Bugfix: Fixed a regression introduced in 2.3.5 where the root daemon
  did not correctly read the configuration file; ignoring the user's
  configured log levels and timeouts.

- Bugfix: Fixed an issue that could cause the user daemon to crash
  during shutdown, as during shutdown it unconditionally attempted to
  close a channel even though the channel might already be closed.

### 2.3.5 (July 15, 2021)

- Feature: Telepresence no longer depends on having an external
  `kubectl` binary, which might not be present for OpenShift users
  (who have `oc` instead of `kubectl`).
- Feature: `skipLogin` can be used in the config.yml to tell the cli not to connect to cloud when using an air-gapped environment.
- Feature: The Telepresence Helm chart now supports installing multiple
  Traffic Managers in multiple namespaces. This will allow operators to
  install Traffic Managers with limited permissions that match the
  permissions restrictions that Telepresence users are subject to.
- Feature: The maximum size of messages that the client can receive over gRPC can now be configured. The gRPC default of 4MB isn't enough
  under some circumstances.
- Change: `TELEPRESENCE_AGENT_IMAGE` and `TELEPRESENCE_REGISTRY` are now only configurable via config.yml.
- Bugfix: Fixed and improved several error messages, to hopefully be
  more helpful.
- Bugfix: Fixed a DNS problem on macOS causing slow DNS lookups when connecting to a local cluster.

### 2.3.4 (July 9, 2021)

- Bugfix: Some log statements that contained garbage instead of a proper IP address now produce the correct address.
- Bugfix: Telepresence will no longer panic when multiple services match a workload.
- Bugfix: The traffic-manager will now accurately determine the service subnet by creating a dummy-service in its own namespace.
- Bugfix: Telepresence connect will no longer try to update the traffic-manager's clusterrole if the live one is identical to the desired one.
- Bugfix: The Telepresence helm chart no longer fails when installing with `--set clientRbac.namespaced=true`

### 2.3.3 (July 7, 2021)

- Feature: Telepresence now supports installing the Traffic Manager
  via Helm. This will make it easy for operators to install and
  configure the server-side components of Telepresence separately from
  the CLI (which in turn allows for better separation of permissions).

- Feature: As the `traffic-manager` can now be installed in any
  namespace via Helm, Telepresence can now be configured to look for
  the traffic manager in a namespace other than `ambassador`. This
  can be configured on a per-cluster basis.

- Feature: `telepresence intercept` now supports a `--to-pod` flag
  that can be used to port-forward sidecars' ports from an intercepted
  pod

- Feature: `telepresence status` now includes more information about
  the root daemon.

- Feature: We now do nightly builds of Telepresence for commits on release/v2 that haven't been tagged and published yet.

- Change: Telepresence no longer automatically shuts down the old
  `api_version=1` `edgectl` daemon. If migrating from such an old
  version of `edgectl` you must now manually shut down the `edgectl`
  daemon before running Telepresence. This was already the case when
  migrating from the newer `api_version=2` `edgectl`.

- Bugfix: The root daemon no longer terminates when the user daemon
  disconnects from its gRPC streams, and instead waits to be
  terminated by the CLI. This could cause problems with things not
  being cleaned up correctly.

- Bugfix: An intercept will survive deletion of the intercepted pod
  provided that another pod is created (or already exists) that can
  take over.

### 2.3.2 (June 18, 2021)

- Feature: The mutator webhook for injecting traffic-agents now
  recognizes a `telepresence.getambassador.io/inject-service-port`
  annotation to specify which port to intercept; bringing the
  functionality of the `--port` flag to users who use the mutator
  webook in order to control Telepresence via GitOps.

- Feature: Outbound connections are now routed through the intercepted
  Pods which means that the connections originate from that Pod from
  the cluster's perspective. This allows service meshes to correctly
  identify the traffic.

- Change: Inbound connections from an intercepted agent are now
  tunneled to the manager over the existing gRPC connection, instead
  of establishing a new connection to the manager for each inbound
  connection. This avoids interference from certain service mesh
  configurations.

- Change: The traffic-manager requires RBAC permissions to list Nodes,
  Pods, and to create a dummy Service in the manager's namespace.

- Change: The on-laptop client no longer requires RBAC permissions to
  list Nodes in the cluster or to create Services, as that
  functionality has been moved to the traffic-manager.

- Bugfix: Telepresence will now detect the pod CIDR ranges even if
  they are not listed in the Nodes.

- Bugfix: The list of cluster subnets that the virtual network
  interface will route is now configured dynamically and will follow
  changes in the cluster.

- Bugfix: Subnets fully covered by other subnets are now pruned
  internally and thus never superfluously added to the laptop's
  routing table.

- Change: The `trafficManagerAPI` timout default has changed from 5
  seconds to 15 seconds, in order to facilitate the extended time it
  takes for the traffic-manager to do its initial discovery of cluster
  info as a result of the above bugfixes.

- Bugfix: On macOS, files generated under `/etc/resolver/` as the
  result of using `include-suffixes` in the cluster config are now
  properly removed on quit.

- Bugfix: Telepresence no longer erroneously terminates connections
  early when sending a large HTTP response from an intercepted
  service.

- Bugfix: When shutting down the user-daemon or root-daemon on the
  laptop, `telepresence quit` and related commands no longer return
  early before everything is fully shut down. Now it can be counted
  on that by the time the command has returned that all the
  side-effects on the laptop have been cleaned up.

### 2.3.1 (June 14, 2021)

- Feature: Agents can now be installed using a mutator webhook
- Feature: DNS resolver can now be configured with respect to what IP addresses that are used, and what lookups that gets sent to the cluster.
- Feature: Telepresence can now be configured to proxy subnets that aren't part of the cluster but only accesible from the cluster.
- Change: The `trafficManagerConnect` timout default has changed from 20 seconds to 60 seconds, in order to facilitate
  the extended time it takes to apply everything needed for the mutator webhook.
- Change: Telepresence is now installable via `brew install datawire/blackbird/telepresence`
- Bugfix: Fix a bug where sometimes large transfers from services on the cluster would hang indefinitely

### 2.3.0 (June 1, 2021)

- Feature: Telepresence is now installable via brew
- Feature: `telepresence version` now also includes the version of the currently running user daemon.
- Change: A TUN-device is used instead of firewall rules for routing outbound connections.
- Change: Outbound connections now use gRPC instead of ssh, and the traffic-manager no longer has a sshd running.
- Change: The traffic-agent no longer has a sshd running. Remote volume mounts use sshfs in slave mode, talking directly to sftp.
- Change: The local DNS now routes the name lookups to intercepted agents or traffic-manager.
- Change: The default log-level for the traffic-manager and the root-daemon was changed from "debug" to "info".
- Change: The command line is now statically-linked, so it is usable on systems with different libc's.
- Bugfix: Using --docker-run no longer fail to mount remote volumes when docker runs as root.
- Bugfix: Fixed a number of race conditions.
- Bugfix: Fix a crash when there is an error communicating with the traffic-manager about Ambassador Cloud.
- Bugfix: Fix a bug where sometimes when displaying or logging a timeout error it fails to determine which configurable timeout is responsible.
- Bugfix: The root-user daemon now respects the timeouts in the normal user's configuration file.

### 2.2.2 (May 17, 2021)

- Feature: Telepresence translates legacy Telepresence commands into viable Telepresence commands.
- Bugfix: Intercepts will only look for agents that are in the same namespace as the intercept.

### 2.2.1 (April 29, 2021)

- Bugfix: Improve `ambassador` namespace detection that was trying to create the namespace even when the namespace existed, which was an undesired RBAC escalation for operators.
- Bugfix: Telepresence will now no longer generate excessive traffic trying repeatedly to exchange auth tokens with Ambassador Cloud. This could happen when upgrading from <2.1.4 if you had an expired `telepresence login` from before upgrading.
- Bugfix: `telepresence login` now correctly handles expired logins, just like all of the other subcommands.

### 2.2.0 (April 19, 2021)

- Feature: `telepresence intercept` now has the option `--docker-run` which will start a docker container with intercepted environment and volume mounts.
- Bugfix: `telepresence uninstall` can once again uninstall agents installed by older versions of Telepresence.
- Feature: Addition of `telepresence current-cluster-id` and `telepresence license` commands for using licenses with the Ambassador extension, primarily in air-gapped environments.

### 2.1.5 (April 12, 2021)

- Feature: When intercepting `--port` now supports specifying a service port or a service name. Previously, only service name was supported.
- Feature: Intercepts using `--mechanism=http` now support mTLS.
- Bugfix: One of the log messages was using the incorrect variable, which led to misleading error messages on `telepresence uninstall`.
- Bugfix: Telepresence no longer generates port names longer than 15 characters.

### 2.1.4 (April 5, 2021)

- Feature: `telepresence status` has been enhanced to provide more information. In particular, it now provides separate information on the daemon and connector processes, as well as showing login status.
- Feature: Telepresence now supports intercepting StatefulSets
- Change: Telepresence necessary RBAC has been refined to support StatefulSets and now requires "get,list,update" for StatefulSets
- Change: Telepresence no longer requires that port 1080 must be available.
- Change: Telepresence now makes use of refresh tokens to avoid requiring the user to manually log in so often.
- Bugfix: Fix race condition that occurred when intercepting a ReplicaSet while another pod was terminating in the same namespace (this fixes a transient test failure)
- Bugfix: Fix error when intercepting a ReplicaSet requires the containerPort to be hidden.
- Bugfix: `telepresence quit` no longer starts the daemon process just to shut it down.
- Bugfix: Telepresence no longer hangs the next time it's run after getting killed.
- Bugfix: Telepresence now does a better job of automatically logging in as necessary, especially with regard to expired logins.
- Bugfix: Telepresence was incorrectly looking across all namespaces for services when intercepting, but now it only looks in the given namespace. This should prevent people from running into "Found multiple services" errors when services with the same selectors existed in other namespaces.

### 2.1.3 (March 29, 2021)

- Feature: Telepresence now supports intercepting ReplicaSets (that aren't owned by a Deployment)
- Change: The --deployment (-d) flag is now --workload (-w), as we start supporting more workloads than just Deployments
- Change: Telepresence necessary RBAC has changed and now requires "delete" for Pods and "get,list,update" for ReplicaSets
- Security: Upgrade to a newer OpenSSL, to address OpenSSL CVE-2021-23840.
- Bugfix: Connecting to Minikube/Hyperkit no longer fails intermittently.
- Bugfix: Telepresence will now make /var/run/secrets/kubernetes.io available when mounting remote volumes.
- Bugfix: Hiccups in the connection to the cluster will no longer cause the connector to shut down; it now retries properly.
- Bugfix: Fix a crash when binary dependencies are missing.
- Bugfix: You can now specify a service when doing an intercept (--service), this is useful if you have two services that select on the same labels (e.g. If using Argo Rollouts do deployments)

### 2.1.2 (March 19, 2021)

- Bugfix: Uninstalling agents now only happens once per deployment instead of once per agent.
- Bugfix: The list command no longer shows agents from namespaces that aren't mapped.
- Bugfix: IPv6 routes now work and don't prevent other pfctl rules being written in macOS
- Bugfix: Pods with `hostname` and/or `subdomain` now get correct DNS-names and routes.
- Change: Service UID was added to InterceptSpec to better link intercepts and services.
- Feature: All timeouts can now be configured in a <user-config-dir>/telepresence/config.yml file

### 2.1.1 (March 12, 2021)

- Bugfix: When looking at the container to intercept, it will check if there's a better match before using a container without containerPorts.
- Bugfix: Telepresence will now map `kube-*` and `ambassador` namespaces by default.
- Bugfix: Service port declarations that lack a TargetPort field will now correctly default to using the Port field instead.
- Bugfix: Several DNS fixes. Notably, introduce a fake "tel2-search" domain that gets replaced with a dynamic DNS search when queried, which fixes DNS for Docker with no `-net host`.
- Change: Improvements to how we report the requirements for volume mounts; notably, if the requirements are not met then it defaults to `--mount=false`.
- Change: There has been substantial code cleanup in the "connector" process.

### 2.1.0 (March 8, 2021)

- Feature: Support headless services (including ExternalName), which you can use if you used "Also Proxy" in telepresence 1.
- Feature: Preview URLs can now set a layer-5 hostname (TLS-SNI and HTTP "Host" header) that is different than the layer-3 hostname (IP-address/DNS-name) that is used to dial to the ingress.
- Feature: The Ingress info will now contain a layer-5 hostname that can be used for TLS-SLI and HTTP "Host" header when accessing a service.
- Feature: Users can choose which port to intercept when intercepting a service with multiple ports.
- Bugfix: Environment variables declared with `envFrom` in the app-container are now propagated correctly to the client during intercept.
- Bugfix: The description of the `--everything` flag for the `uninstall` command was corrected.
- Bugfix: Connecting to a large cluster could take a very long time and even make the process hang. This is no longer the case.
- Bugfix: Telepresence now explicitly requires macFUSE version 4.0.5 or higher for macOS.
- Bugfix: A `tail -F <daemon log file>` no longer results in a "Permission denied" when reconnecting to the cluster.
- Change: The telepresence daemon will no longer use port 1234 for the firewall-to-SOCKS server, but will instead choose an available port dynamically.
- Change: On connect, telepresence will no longer suggest the `--mapped-namespaces` flag when the user connects to a large cluster.

### 2.0.3 (February 24, 2021)

- Feature: There is now an extension mechanism where you can tell Telepresence about different agents and what arguments they support. The new `--mechanism` flag can explicitly identify which extension to use.
- Feature: An intercept of `NAME` that is made using `--namespace=NAMESPACE` but not using `--deployment` will use `NAME` as the name of the deployment and `NAME-NAMESPACE` as the name of the intercept.
- Feature: Declare a local-only intercept for the purpose of getting direct outbound access to the intercept's namespace using boolean flag `--local-only`.
- Bugfix: Fix a regression in the DNS resolver that prevented name resolution using NAME.NAMESPACE. Instead, NAME.NAMESPACE.svc.cluster.local was required.
- Bugfix: Fixed race-condition in the agent causing attempts to dial to `:0`.
- Bugfix: It is now more strict about which agent versions are acceptable and will be more eager to apply upgrades.
- Change: Related to things now being in extensions, the `--match` flag has been renamed to `--http-match`.
- Change: Cluster connection timeout has been increased from 10s to 20s.
- Change: On connect, if telepresence detects a large cluster, it will suggest the `--mapped-namespaces` flag to the user as a way to speed it up.
- Change: The traffic-agent now has a readiness probe associated with its container.

### 2.0.2 (February 18, 2021)

- Feature: Telepresence is now capable of forwarding the intercepted Pod's volume mounts (as Telepresence 0.x did) via the `--mount` flag to `telepresence intercept`.
- Feature: Telepresence will now allow simultaneous intercepts in different namespaces.
- Feature: It is now possible for a user to limit what namespaces that will be used by the DNS-resolver and the NAT.
- Bugfix: Fix the kubectl version number check to handle version numbers with a "+" in them.
- Bugfix: Fix a bug with some configurations on macOS where we clash with mDNSResponder's use of port 53.

### 2.0.1 (February 9, 2021)

- Feature: Telepresence is now capable of forwarding the environment variables of an intercepted service (as Telepresence 0.x did) and emit them to a file as text or JSON. The environment variables will also be propagated to any command started by doing a `telepresence intercept nnn -- <command>`.
- Bugfix: A bug causing a failure in the Telepresence DNS resolver when attempting to listen to the Docker gateway IP was fixed. The fix affects Windows using a combination of Docker and WSL2 only.
- Bugfix: Telepresence now works correctly while OpenVPN is running on macOS.
- Change: The background processes `connector` and `daemon` will now use rotating logs and a common directory.
  - macOS: `~/Library/Logs/telepresence/`
  - Linux: `$XDG_CACHE_HOME/telepresence/logs/` or `$HOME/.cache/telepresence/logs/`
